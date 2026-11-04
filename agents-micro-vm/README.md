# agents-micro-vm

A small, measurable version of the pattern Google described for [Agent Compute Engine](https://cloud.google.com/blog/topics/ai-infrastructure/agent-compute-engine-deep-dive): one Firecracker microVM per agent session, warm snapshots shared copy-on-write, idle-aware checkpointing, snapshot-fork, and a guest with no NIC whose only route out is vsock to a host proxy that holds the credentials.

Companion code for the post "One MicroVM per Agent" on antonau.tech.

```
 host                                                   guest (no NIC)
 ┌──────────────────────────────────────────┐           ┌─────────────────────────────┐
 │ orchestrator / bench                     │  vsock    │ guest-agent (PID 1)         │
 │   vm.Restore(snapshot) ×N ───────────────┼──────────►│   :10000 exec/ping/status   │
 │   egress.Proxy per VM  ◄─────────────────┼───────────┤   127.0.0.1:3128 → host     │
 │     allowlist + Bearer token injection   │  vsock    │   (HTTP_PROXY for tools)    │
 └──────────────────────────────────────────┘           └─────────────────────────────┘
```

## Layout

| Path | What |
|---|---|
| `cmd/guest-agent` | Runs inside the VM as init. Mounts, loopback, exec over vsock, reports in-flight and background work, forwards `127.0.0.1:3128` to the host. |
| `cmd/orchestrator` | `template` (cold boot → warm → snapshot), `fork` (N restores of one snapshot), `session` (idle-aware pause/hibernate). |
| `cmd/bench` | Cold-boot, restore, fork, pause/resume, hibernate/wake latency and per-VM memory → CSV. |
| `internal/vm` | firecracker-go-sdk wrapper. Raw API calls where the SDK lags Firecracker (`vsock_override`, virtio-pmem). |
| `internal/egress` | The egress proxy (~200 lines) and its tests. |
| `internal/idle` | Idle detection and checkpoint/restore for one session. |

## Host requirements

- **Linux with KVM**, x86_64 or aarch64. Bare metal for numbers you publish (an AWS `.metal` instance, a Hetzner dedicated box, your own machine). Nested virtualization works for development but its latency is not representative.
- Host kernel: Firecracker supports 5.10, 6.1 and 6.18 host kernels as of v1.16 ([release notes](https://github.com/firecracker-microvm/firecracker/releases)).
- **cgroups v2**. Firecracker's [snapshot docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md) call out high restore latency on v1.
- Read/write access to `/dev/kvm` (`kvm` group or an ACL), `sudo` for building the rootfs and for `-drop-caches`.
- Go 1.24+, `curl`, `squashfs-tools`, `e2fsprogs`.
- About 3 GB disk, plus one guest-memory-sized file per snapshot (512 MiB by default).
- **Firecracker v1.16 or newer.** `vsock_override` on snapshot load, which forks depend on, arrived in v1.16.0. The setup script pins v1.17.0.

The firecracker-go-sdk only builds on Linux. On macOS, `GOOS=linux go build ./...` and `go test ./internal/egress/` work; nothing else does.

## Setup

```bash
git clone <this repo> && cd agents-micro-vm

# 1. Check KVM/cgroups/swap, download Firecracker v1.17.0 and the Firecracker CI 6.1 guest kernel.
./scripts/setup-host.sh

# 2. Build the rootfs: Firecracker CI Ubuntu 24.04 + curl + python3 + guest-agent as /usr/local/bin/guest-agent.
./scripts/build-rootfs.sh          # EXTRA_PKGS="" to skip apt, SIZE_MIB=2048 for more room

# 3. Build the host tools.
go build -o bin/ ./cmd/orchestrator ./cmd/bench
export PATH=$PWD/bin:$PATH
```

The setup script warns if the guest kernel lacks `CONFIG_VIRTIO_VSOCKETS`, `CONFIG_VIRTIO_PMEM`, `CONFIG_FS_DAX` or `CONFIG_VMGENID`. The Firecracker CI 6.1 config has all four (checked 2026-10-26 against `firecracker-ci/20261023-dd7c24b0d165-0`).

All commands take `-fc bin/firecracker -kernel images/vmlinux -rootfs images/rootfs.ext4`. Those are the defaults except `-fc`, so either keep `bin/` on `PATH` or pass `-fc`.

## Use it

```bash
# Cold boot, pre-import some Python, snapshot to snapshots/warm/{mem,state}.
orchestrator template -fc bin/firecracker -warm "python3 -c 'import json, ssl, sqlite3, http.client'"

# Eight forks of that snapshot. Each prints different random bytes: VMGenID made
# the guest kernel reseed its RNG on restore.
orchestrator fork -fc bin/firecracker -n 8

# Keep 32 forks alive and look at memory yourself:
orchestrator fork -fc bin/firecracker -n 32 -keep
#   in another shell: sudo smem -P firecracker -c "pid rss pss uss"

# Idle-aware session: hibernate after 3 s idle, wake on the next command.
orchestrator session -fc bin/firecracker -idle 3s -mode hibernate
> python3 -c 'print(sum(range(10**6)))'
> sleep 20 &          # background work: the session will not sleep until it exits
```

### Egress with injected credentials

The guest has no network device. `curl` in the guest picks up `HTTP_PROXY=http://127.0.0.1:3128`. The guest agent forwards that over vsock to a per-VM proxy on the host.

```bash
orchestrator session -fc bin/firecracker \
  -allow pypi.org -allow files.pythonhosted.org \
  -inject 'api.github.com=exec:gh auth token'
> curl -s http://api.github.com/user | head -5      # note http://, the proxy upgrades to TLS and adds the token
> env | grep -i -E 'token|auth' ; echo "exit=$?"     # nothing: the guest never saw it
> curl -s https://example.com                          # 403: not on the allowlist
> curl -sI https://pypi.org/simple/ | head -1          # allowed: opaque CONNECT tunnel, no credential
```

Credentialed hosts must use `http://` from the guest. The proxy refuses `CONNECT` to them, because injecting a header into TLS would require a MITM CA inside the guest. See `internal/egress/proxy.go` for the reasoning.

## Benchmarks

The suite used for the article, with identical flags on every host so results compare:

```bash
./scripts/run-bench.sh my-host          # results/my-host/{block,pmem,work,coldcache}/
```

It runs:
1. block rootfs: cold, restore, fork (N = 1/4/16/64, 3 reps), pause, hibernate;
2. the same phases with the rootfs on virtio-pmem + DAX;
3. forks of 16 after each one writes 64 MiB, to show copy-on-write savings eroding;
4. restore with the host page cache dropped (`sudo`).

`ITERS` (default 200) and `FORKS` env vars override the defaults. With a 512 MiB guest, the 64-fork batches want about 4 GiB of free host RAM, and the suite takes 20–40 minutes depending on the host.

Running `bench` by hand:

```bash
bin/bench -fc bin/firecracker -iters 200 -forks 1,4,16,64 -fork-reps 3 \
  -warm "python3 -c 'import json, ssl, sqlite3, http.client'" -out results/block-512m
bin/bench -fc bin/firecracker -rootfs-mode pmem -out results/pmem-512m ...
sudo bin/bench -fc bin/firecracker -phases restore -drop-caches -out results/coldcache
```

`sudo` is only needed for `-drop-caches`.

### On a Mac (Apple Silicon M3 or newer)

macOS 15+ exposes nested virtualization to Linux VMs, so you can run everything inside Lima. The numbers are real, but they're **nested-virt numbers**, so label them as such. What I saw on an M4 with a 6-vCPU Lima VM:

- Serial operations work and are stable. Most restore time is guest-side, after Firecracker has already loaded and resumed the snapshot.
- **Concurrent boots don't scale, and then they collapse.** 4 VMs starting at once each took about 4x longer than one; 8 took 8–10x. At 12 at once (cold boots and restores alike), none of them became ready. Run the suite with `FORKS=1,4,8` there.
- **Starting VMs on a crowded host is erratic.** In the density run (64 forks, at most 8 starting at a time), individual forks took anywhere from 2 s to over 2 minutes to become ready. The memory numbers from that run are fine, because they're read after all 64 are up. The latency numbers from it aren't worth quoting.

```bash
brew install lima
limactl create --name=fc --vm-type=vz --cpus=6 --memory=14 --disk=40 \
  --set '.nestedVirtualization=true' --mount-none template:ubuntu-24.04
limactl start fc
limactl shell fc        # then: add yourself to the kvm group, re-login (limactl stop/start), follow Setup
```

Outputs in `-out`:

| File | Contents |
|---|---|
| `raw.csv` | One row per measurement: `phase, fork_n, iter, vm, latency_ms, rss_kib, pss_kib, shared_kib, private_kib, private_dirty_kib, rootfs_mode, mem_mib, vcpus` |
| `summary.csv` | Per phase and fork size: samples, p50/p90/p99/min/max ms, mean RSS/PSS/private KiB |
| `memory.csv` | Per fork batch: host `MemAvailable` drop divided by N, which is the honest per-VM cost |
| `meta.json` | Host kernel, CPU, Firecracker version, guest size, flags. Publish it with the numbers. |

What the phases measure. Each latency runs from the start of the operation until the guest agent answers a ping over vsock.

| Phase | Measures |
|---|---|
| `cold` | spawn Firecracker, configure, boot kernel, init, agent listening |
| `*_vmm` (`cold_vmm`, `restore_vmm`, `fork_vmm`, `wake_vmm`) | just the VMM side: the SDK call returned (process up, API calls done, VM started or snapshot loaded and resumed). The rest of the phase's latency is the guest: kernel boot, or page faults on restored memory. |
| `snapshot` | pause + full snapshot of the warmed template (once) |
| `restore` / `restore_coldcache` | spawn Firecracker, load snapshot (File backend, MAP_PRIVATE), resume, agent reachable |
| `fork` | per VM in a batch of N concurrent restores, measured from batch start |
| `fork_batch` | wall time until all N are ready |
| `pause` / `resume` | PATCH /vm Paused, then Resumed + one ping |
| `hibernate` / `wake` | full snapshot + kill VMM / restore from that checkpoint |

Memory is read from `/proc/<pid>/smaps_rollup` of each Firecracker process, at the moment it became ready for `cold`/`restore`/`wake`, and after `-settle` (default 2 s) for forks. RSS counts shared pages in every process. Use PSS or private memory for per-VM cost, and `memory.csv` as a cross-check.

Percentiles are nearest-rank. With `-iters 50`, p99 is the second-largest sample, so don't report p99 below a few hundred samples.

## Known limits

- **Full snapshots only.** Hibernating writes the whole guest memory each time. Firecracker's diff snapshots are still developer preview.
- **Same snapshot, same secrets.** Anything the template had in memory, every fork has. The kernel RNG is reseeded via VMGenID, but userspace PRNG state, cached tokens and UUID generators are not. Don't put secrets in templates.
- **The vsock connection does not survive a snapshot.** Firecracker closes guest vsock connections on resume, so the protocol uses one connection per call.
- **Clock.** A restored guest wakes with the clock from snapshot time. The agent resets it from the host timestamp on every request. Firecracker's `clock_realtime` load option is the in-VMM alternative on x86_64.
- **Sharing is a side channel.** Firecracker's own [pmem docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/pmem.md) advise against sharing one backing file across VMs for that reason. Shared snapshot memory has the same property. Share across one tenant's sessions, not across tenants.
- **No jailer, no seccomp tuning, no cgroups per VM.** See the post's "production" section.
