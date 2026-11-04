#!/usr/bin/env bash
# Builds images/rootfs.ext4: Firecracker's CI Ubuntu image + our guest agent as init.
# Needs sudo (unsquashfs preserves root ownership; chroot for extra packages).
set -euo pipefail
cd "$(dirname "$0")/.."

ARCH="$(uname -m)"
GOARCH=amd64; [[ "$ARCH" == aarch64 ]] && GOARCH=arm64
S3="https://s3.amazonaws.com/spec.ccfc.min"
SIZE_MIB="${SIZE_MIB:-1024}"                 # keep a multiple of 2 for pmem alignment
EXTRA_PKGS="${EXTRA_PKGS:-curl python3}"     # installed via chroot; set empty to skip
[[ -f images/ci-prefix ]] || { echo "run scripts/setup-host.sh first" >&2; exit 1; }
CI_PREFIX="$(cat images/ci-prefix)"

if [[ ! -f images/ubuntu.squashfs ]]; then
  KEY="$(curl -fsSL "${S3}?list-type=2&prefix=${CI_PREFIX}${ARCH}/ubuntu-" \
    | grep -oE "${CI_PREFIX}${ARCH}/ubuntu-[0-9.]+\.squashfs" | sort -V | tail -1)"
  echo "downloading $KEY"
  curl -fsSL -o images/ubuntu.squashfs "${S3}/${KEY}"
fi

echo "building guest-agent (linux/$GOARCH, static)"
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -trimpath -ldflags='-s -w' -o images/guest-agent ./cmd/guest-agent

TREE=images/rootfs-tree
sudo rm -rf "$TREE"
sudo unsquashfs -q -d "$TREE" images/ubuntu.squashfs

if [[ -n "$EXTRA_PKGS" ]]; then
  echo "installing into rootfs: $EXTRA_PKGS"
  # The CI image is trimmed: apt needs a writable /tmp and its cache dirs back.
  sudo mkdir -p "$TREE/tmp" "$TREE/var/cache/apt/archives/partial" "$TREE/var/lib/apt/lists/partial" "$TREE/var/log/apt"
  sudo chmod 1777 "$TREE/tmp"
  sudo cp /etc/resolv.conf "$TREE/etc/resolv.conf"
  for d in proc sys dev; do sudo mount --bind "/$d" "$TREE/$d"; done
  trap 'for d in proc sys dev; do sudo umount -l "'"$TREE"'/$d" 2>/dev/null || true; done' EXIT
  sudo chroot "$TREE" /bin/sh -c "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $EXTRA_PKGS ca-certificates && apt-get clean && rm -rf /var/lib/apt/lists/*"
  for d in proc sys dev; do sudo umount -l "$TREE/$d"; done
  trap - EXIT
fi

sudo install -m 0755 images/guest-agent "$TREE/usr/local/bin/guest-agent"
sudo mkdir -p "$TREE"/{work,proc,sys,dev,tmp,run}
# The guest has no NIC and no DNS; the proxy resolves names on the host.
echo "127.0.0.1 localhost sandbox" | sudo tee "$TREE/etc/hosts" >/dev/null

rm -f images/rootfs.ext4
truncate -s "${SIZE_MIB}M" images/rootfs.ext4
# 4 KiB blocks: ext4 DAX requires block size == page size.
sudo mkfs.ext4 -q -F -b 4096 -L rootfs -d "$TREE" images/rootfs.ext4
sudo chown "$(id -u):$(id -g)" images/rootfs.ext4
sudo rm -rf "$TREE"
echo "images/rootfs.ext4 ready ($(du -h --apparent-size images/rootfs.ext4 | cut -f1))"
