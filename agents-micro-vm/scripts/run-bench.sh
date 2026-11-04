#!/usr/bin/env bash
# The full benchmark suite used for the article. Same flags everywhere so
# results from different hosts are comparable.
#
#   ./scripts/run-bench.sh [label]      # results land in results/<label>/
#
# Runs as your user, except the dropped-page-cache restore, which needs sudo.
set -euo pipefail
cd "$(dirname "$0")/.."

LABEL="${1:-$(hostname -s)}"
OUT="results/$LABEL"
RUN_DIR="${RUN_DIR:-/tmp/amv-bench}"
ITERS="${ITERS:-200}"
# All-at-once fork batches. Under nested virtualization, concurrently booting
# guests slow down roughly linearly and then livelock: on a 6-vCPU Lima VM
# (Apple M4) 10 at once worked and 12 at once never became ready, for cold
# boots and restores alike. Keep FORKS below ~1.5x the host's vCPUs there,
# e.g. FORKS=1,4,8. The density step below starts at most 8 at a time.
FORKS="${FORKS:-1,4,16,64}"
WARM="python3 -c 'import json, ssl, sqlite3, http.client'"
# Synthetic agent work: write one byte into every page of 64 MiB, so each
# fork dirties ~64 MiB of its copy-on-write memory.
WORK="python3 -c 'b=bytearray(64<<20); b[::4096]=bytes(len(b)//4096)'"

[[ -x bin/firecracker && -x bin/bench ]] || { echo "run setup-host.sh, build-rootfs.sh and go build -o bin/ ./cmd/... first" >&2; exit 1; }
if pgrep -x firecracker >/dev/null; then
  echo "firecracker processes already running; they would skew CPU and memory numbers:" >&2
  pgrep -a -x firecracker >&2
  exit 1
fi
mkdir -p "$OUT"
common=(-fc bin/firecracker -warm "$WARM")

echo "== 1/5 block rootfs: cold, restore, fork, pause, hibernate"
bin/bench "${common[@]}" -run-dir "$RUN_DIR" -rootfs-mode block -iters "$ITERS" -forks "$FORKS" -fork-reps 3 \
  -snap snapshots/bench-block -out "$OUT/block" > "$OUT/block.log"

echo "== 2/5 pmem + DAX rootfs: same phases"
bin/bench "${common[@]}" -run-dir "$RUN_DIR" -rootfs-mode pmem -iters "$ITERS" -forks "$FORKS" -fork-reps 3 \
  -snap snapshots/bench-pmem -out "$OUT/pmem" > "$OUT/pmem.log"

echo "== 3/5 density: 64 forks, at most 8 starting at once (block, then pmem)"
for mode in block pmem; do
  bin/bench "${common[@]}" -run-dir "$RUN_DIR" -rootfs-mode $mode -phases fork -forks 64 -fork-reps 3 -fork-parallel 8 \
    -snap snapshots/bench-$mode -out "$OUT/density-$mode" > "$OUT/density-$mode.log"
done

echo "== 4/5 forks after 64 MiB of work each (block, N=16)"
bin/bench "${common[@]}" -run-dir "$RUN_DIR" -rootfs-mode block -phases fork -forks 16 -fork-reps 3 -fork-parallel 8 -work "$WORK" \
  -snap snapshots/bench-block -out "$OUT/work" > "$OUT/work.log"

echo "== 5/5 restore with dropped page cache (sudo)"
sudo bin/bench "${common[@]}" -run-dir "${RUN_DIR}-root" -rootfs-mode block -phases restore -drop-caches -iters "$ITERS" \
  -snap "${RUN_DIR}-root/snap" -out "$OUT/coldcache" > "$OUT/coldcache.log"
sudo chown -R "$(id -u):$(id -g)" "$OUT"

echo "done: $OUT/{block,pmem,density-block,density-pmem,work,coldcache}/{raw,summary,memory}.csv + meta.json"
