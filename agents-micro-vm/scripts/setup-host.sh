#!/usr/bin/env bash
# Checks the host and downloads Firecracker + a guest kernel into ./bin and ./images.
set -euo pipefail
cd "$(dirname "$0")/.."

FC_VERSION="${FC_VERSION:-v1.17.0}"
KERNEL_SERIES="${KERNEL_SERIES:-6.1}"   # Firecracker CI also publishes 5.10 and 6.18
ARCH="$(uname -m)"
S3="https://s3.amazonaws.com/spec.ccfc.min"

fail() { echo "FAIL: $*" >&2; exit 1; }
warn() { echo "WARN: $*" >&2; }

[[ "$(uname -s)" == Linux ]] || fail "Linux host required"
[[ "$ARCH" == x86_64 || "$ARCH" == aarch64 ]] || fail "x86_64 or aarch64 required, got $ARCH"
[[ -e /dev/kvm ]] || fail "/dev/kvm missing: enable virtualization in BIOS, or use a .metal / nested-virt instance"
[[ -r /dev/kvm && -w /dev/kvm ]] || fail "no rw access to /dev/kvm: sudo usermod -aG kvm \$USER (then re-login) or sudo setfacl -m u:\$USER:rw /dev/kvm"
[[ "$(stat -fc %T /sys/fs/cgroup)" == cgroup2fs ]] || warn "cgroups v1 detected; Firecracker documents high restore latency on v1"
if [[ -n "$(swapon --noheadings 2>/dev/null)" ]]; then
  warn "swap is on; memory numbers will be noisier (consider: sudo swapoff -a)"
fi
virt="$(systemd-detect-virt 2>/dev/null || true)"
if [[ -n "$virt" && "$virt" != none ]] || grep -q hypervisor /proc/cpuinfo; then
  warn "this host is itself a VM (${virt:-nested}); latency numbers won't match bare metal"
fi
command -v go >/dev/null || fail "Go 1.24+ required"
for t in curl unsquashfs mkfs.ext4; do command -v "$t" >/dev/null || fail "missing $t (apt install curl squashfs-tools e2fsprogs)"; done

mkdir -p bin images

if [[ ! -x bin/firecracker ]]; then
  echo "downloading firecracker $FC_VERSION"
  curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-${ARCH}.tgz" | tar -xz -C bin
  mv "bin/release-${FC_VERSION}-${ARCH}/firecracker-${FC_VERSION}-${ARCH}" bin/firecracker
  mv "bin/release-${FC_VERSION}-${ARCH}/jailer-${FC_VERSION}-${ARCH}" bin/jailer
  rm -rf "bin/release-${FC_VERSION}-${ARCH}"
fi
bin/firecracker --version | head -1

# Latest Firecracker CI artifact set (prefixes are firecracker-ci/YYYYMMDD-<sha>-N/).
CI_PREFIX="$(curl -fsSL "${S3}?list-type=2&prefix=firecracker-ci/&delimiter=/" \
  | grep -oE 'firecracker-ci/[0-9]{8}-[^/<]+/' | sort -u | tail -1)"
[[ -n "$CI_PREFIX" ]] || fail "could not list Firecracker CI artifacts"
echo "$CI_PREFIX" > images/ci-prefix

if [[ ! -f images/vmlinux ]]; then
  KEY="$(curl -fsSL "${S3}?list-type=2&prefix=${CI_PREFIX}${ARCH}/vmlinux-${KERNEL_SERIES}." \
    | grep -oE "${CI_PREFIX}${ARCH}/vmlinux-${KERNEL_SERIES}\.[0-9]+" | sort -V | tail -1)"
  [[ -n "$KEY" ]] || fail "no vmlinux-${KERNEL_SERIES}.x under $CI_PREFIX"
  echo "downloading $KEY"
  curl -fsSL -o images/vmlinux "${S3}/${KEY}"
  curl -fsSL -o images/vmlinux.config "${S3}/${KEY}.config"
  echo "${KEY##*/}" > images/vmlinux.version
fi

for opt in VIRTIO_VSOCKETS VIRTIO_PMEM FS_DAX VMGENID; do
  grep -q "^CONFIG_${opt}=y" images/vmlinux.config || warn "guest kernel lacks CONFIG_${opt}=y"
done
echo "host OK; kernel $(cat images/vmlinux.version). Next: scripts/build-rootfs.sh"
