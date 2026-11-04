// bench measures cold boot, restore, fork, pause/resume and hibernate/wake
// latency, plus memory per VM, and writes CSV.
//
// Latency is always "start of operation -> guest agent answers a ping over
// vsock". That's what an agent harness waits for before it can dispatch a
// tool call, so it's the number that matters, and it includes the VMM
// process spawn.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agents-micro-vm/internal/cliflags"
	"agents-micro-vm/internal/vm"
)

type row struct {
	Phase   string
	ForkN   int
	Iter    int
	VM      string
	Latency time.Duration
	Mem     vm.MemStats
	HasMem  bool
}

type bench struct {
	cfg        vm.Config
	snap       vm.Snapshot
	iters      int
	hibIters   int
	forkNs     []int
	forkReps   int
	forkPar    int
	dropCaches bool
	settle     time.Duration
	work       string

	rows    []row
	memRows []memRow
}

type memRow struct {
	Phase              string
	ForkN, Rep         int
	MemAvailDeltaPerVM int64 // KiB
}

func main() {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	vf := cliflags.RegisterVM(fs)
	b := &bench{}
	fs.IntVar(&b.iters, "iters", 50, "iterations for cold/restore/pause (use >=200 if you report p99)")
	fs.IntVar(&b.hibIters, "hibernate-iters", 10, "iterations for hibernate/wake (each writes a full memory snapshot)")
	forks := fs.String("forks", "1,4,16,64", "fork batch sizes")
	fs.IntVar(&b.forkReps, "fork-reps", 3, "repetitions per fork batch size")
	fs.IntVar(&b.forkPar, "fork-parallel", 0, "start at most this many forks at once (0 = all at once). Bounded runs record phase fork_staggered, timed per VM, for density measurements")
	fs.BoolVar(&b.dropCaches, "drop-caches", false, "drop host page cache before each restore (needs root)")
	fs.DurationVar(&b.settle, "settle", 2*time.Second, "wait after a fork batch is ready before reading memory")
	fs.StringVar(&b.work, "work", "", "command each fork runs before the memory reading (shows private page growth)")
	warm := fs.String("warm", "", "warm-up command run before the template snapshot")
	phases := fs.String("phases", "cold,restore,fork,pause,hibernate", "phases to run")
	out := fs.String("out", "results", "output directory")
	fs.Parse(os.Args[1:])

	cur, curOut, curWarm = b, *out, *warm
	var err error
	if b.cfg, err = vf.Config(); err != nil {
		fatal(err)
	}
	for _, s := range strings.Split(*forks, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fatal(fmt.Errorf("-forks: %w", err))
		}
		b.forkNs = append(b.forkNs, n)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	ctx := context.Background()

	want := map[string]bool{}
	for _, p := range strings.Split(*phases, ",") {
		want[strings.TrimSpace(p)] = true
	}
	if want["cold"] {
		b.cold(ctx)
	}
	// Every other phase starts from the warm template, so build it here,
	// with this run's settings, rather than trusting one left on disk.
	b.template(ctx, vf.SnapDir, *warm)
	if want["restore"] {
		b.restore(ctx)
	}
	if want["fork"] {
		b.fork(ctx)
	}
	if want["pause"] {
		b.pauseResume(ctx)
	}
	if want["hibernate"] {
		b.hibernate(ctx)
	}

	must(b.writeAll(*out, *warm))
	fmt.Printf("wrote %s/{raw,summary,memory}.csv and meta.json\n", *out)
}

func (b *bench) writeAll(out, warm string) error {
	if err := b.writeRaw(filepath.Join(out, "raw.csv")); err != nil {
		return err
	}
	if len(b.rows) > 0 {
		if err := b.writeSummary(filepath.Join(out, "summary.csv")); err != nil {
			return err
		}
	}
	if err := b.writeMem(filepath.Join(out, "memory.csv")); err != nil {
		return err
	}
	return writeMeta(filepath.Join(out, "meta.json"), b, warm)
}

func (b *bench) record(r row) {
	b.rows = append(b.rows, r)
	fmt.Printf("%-10s n=%-3d iter=%-3d %-12s %8.2fms\n", r.Phase, r.ForkN, r.Iter, r.VM, ms(r.Latency))
}

type boot struct {
	vm  *vm.VM
	vmm time.Duration // start() returned: VMM up, guest started or snapshot loaded and resumed
	lat time.Duration // guest agent answered
	mem vm.MemStats
}

// bootTimed runs start, then waits for the guest agent, and returns the VM
// with memory read at the moment it became ready. The gap between vmm and
// lat is guest-side: kernel boot on a cold start, page faults on a restore.
func bootTimed(ctx context.Context, start func() (*vm.VM, error)) (boot, error) {
	t0 := time.Now()
	v, err := start()
	if err != nil {
		return boot{}, err
	}
	b := boot{vm: v, vmm: time.Since(t0)}
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := v.WaitReady(rctx); err != nil {
		v.Stop()
		return boot{}, err
	}
	b.lat = time.Since(t0)
	b.mem, _ = vm.ReadMemStats(v.PID)
	return b, nil
}

func (b *bench) recordBoot(phase string, iter int, bt boot) {
	b.record(row{Phase: phase + "_vmm", Iter: iter, VM: bt.vm.ID, Latency: bt.vmm})
	b.record(row{Phase: phase, Iter: iter, VM: bt.vm.ID, Latency: bt.lat, Mem: bt.mem, HasMem: true})
}

func (b *bench) cold(ctx context.Context) {
	for i := range b.iters {
		bt, err := bootTimed(ctx, func() (*vm.VM, error) { return vm.ColdBoot(b.cfg, "cold") })
		if err != nil {
			fatal(fmt.Errorf("cold boot %d: %w", i, err))
		}
		b.recordBoot("cold", i, bt)
		bt.vm.Stop()
	}
}

func (b *bench) template(ctx context.Context, dir, warm string) {
	bt, err := bootTimed(ctx, func() (*vm.VM, error) { return vm.ColdBoot(b.cfg, "template") })
	if err != nil {
		fatal(fmt.Errorf("template boot: %w", err))
	}
	v := bt.vm
	defer v.Stop()
	if warm != "" {
		if _, err := v.Exec(ctx, warm, 10*time.Minute); err != nil {
			fatal(fmt.Errorf("warm-up: %w", err))
		}
	}
	t0 := time.Now()
	if b.snap, err = v.Snapshot(ctx, dir); err != nil {
		fatal(err)
	}
	b.record(row{Phase: "snapshot", VM: "template", Latency: time.Since(t0)})
	abs, _ := filepath.Abs(dir)
	b.snap = vm.Snapshot{MemPath: filepath.Join(abs, "mem"), StatePath: filepath.Join(abs, "state")}
}

func (b *bench) restore(ctx context.Context) {
	phase := "restore"
	if b.dropCaches {
		phase = "restore_coldcache"
	}
	for i := range b.iters {
		if b.dropCaches {
			if err := vm.DropCaches(); err != nil {
				fatal(fmt.Errorf("drop caches: %w", err))
			}
		}
		bt, err := bootTimed(ctx, func() (*vm.VM, error) { return vm.Restore(b.cfg, b.snap, "restore") })
		if err != nil {
			fatal(fmt.Errorf("restore %d: %w", i, err))
		}
		b.recordBoot(phase, i, bt)
		bt.vm.Stop()
	}
}

// fork restores N VMs from the same snapshot at once. Each VM's latency is
// measured from the batch start, so it includes contention with its siblings.
func (b *bench) fork(ctx context.Context) {
	phase := "fork"
	if b.forkPar > 0 {
		phase = "fork_staggered"
	}
	for _, n := range b.forkNs {
		// Ready timeout grows with N: on a contended host (nested virt, few
		// cores) a big batch is slow, and slow is a result, not an error.
		readyTimeout := 120*time.Second + time.Duration(n)*10*time.Second
		var sem chan struct{}
		if b.forkPar > 0 {
			sem = make(chan struct{}, b.forkPar)
		}
		for rep := range b.forkReps {
			time.Sleep(time.Second) // let the previous batch's memory go back
			before, _ := vm.MemAvailableKiB()

			vms := make([]*vm.VM, n)
			lats := make([]time.Duration, n)
			vmmLats := make([]time.Duration, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			t0 := time.Now()
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					start := t0
					if sem != nil {
						sem <- struct{}{}
						defer func() { <-sem }()
						start = time.Now() // staggered: time each VM from its own launch
					}
					id := fmt.Sprintf("fork-%03d", i)
					v, err := vm.Restore(b.cfg, b.snap, id)
					if err != nil {
						errs[i] = err
						return
					}
					vms[i] = v
					vmmLats[i] = time.Since(start)
					rctx, cancel := context.WithTimeout(ctx, readyTimeout)
					defer cancel()
					errs[i] = v.WaitReady(rctx)
					lats[i] = time.Since(start)
				}()
			}
			wg.Wait()
			batch := time.Since(t0)
			for i, err := range errs {
				if err != nil {
					stopAll(vms)
					fatal(fmt.Errorf("fork n=%d vm %d: %w", n, i, err))
				}
			}

			if b.work != "" {
				// Same concurrency limit as the launches: N guests all busy at
				// once is exactly what -fork-parallel exists to avoid.
				var ww sync.WaitGroup
				werrs := make([]error, n)
				for i, v := range vms {
					ww.Add(1)
					go func() {
						defer ww.Done()
						if sem != nil {
							sem <- struct{}{}
							defer func() { <-sem }()
						}
						resp, err := v.Exec(ctx, b.work, 5*time.Minute)
						if err == nil && resp.ExitCode != 0 {
							err = fmt.Errorf("exit %d: %s", resp.ExitCode, resp.Stderr)
						}
						werrs[i] = err
					}()
				}
				ww.Wait()
				for i, err := range werrs {
					if err != nil {
						stopAll(vms)
						fatal(fmt.Errorf("work on fork %d: %w", i, err))
					}
				}
			}
			time.Sleep(b.settle)
			after, _ := vm.MemAvailableKiB()

			for i, v := range vms {
				mem, _ := vm.ReadMemStats(v.PID)
				b.record(row{Phase: phase + "_vmm", ForkN: n, Iter: rep, VM: v.ID, Latency: vmmLats[i]})
				b.record(row{Phase: phase, ForkN: n, Iter: rep, VM: v.ID, Latency: lats[i], Mem: mem, HasMem: true})
			}
			b.record(row{Phase: phase + "_batch", ForkN: n, Iter: rep, VM: "batch", Latency: batch})
			b.memRows = append(b.memRows, memRow{Phase: phase, ForkN: n, Rep: rep, MemAvailDeltaPerVM: (before - after) / int64(n)})
			stopAll(vms)
		}
	}
}

func (b *bench) pauseResume(ctx context.Context) {
	bt, err := bootTimed(ctx, func() (*vm.VM, error) { return vm.Restore(b.cfg, b.snap, "pause") })
	if err != nil {
		fatal(err)
	}
	v := bt.vm
	defer v.Stop()
	for i := range b.iters {
		t0 := time.Now()
		must(v.Pause(ctx))
		b.record(row{Phase: "pause", Iter: i, VM: v.ID, Latency: time.Since(t0)})
		t1 := time.Now()
		must(v.Resume(ctx))
		if _, err := v.Ping(ctx); err != nil {
			fatal(err)
		}
		b.record(row{Phase: "resume", Iter: i, VM: v.ID, Latency: time.Since(t1)})
	}
}

// hibernate is the "zero active resources" path: snapshot the running VM to
// disk, kill the VMM, later restore from that checkpoint.
func (b *bench) hibernate(ctx context.Context) {
	snap := b.snap
	var prevDir string
	for i := range b.hibIters {
		bt, err := bootTimed(ctx, func() (*vm.VM, error) { return vm.Restore(b.cfg, snap, "hib") })
		if err != nil {
			fatal(err)
		}
		v := bt.vm
		if i > 0 {
			b.recordBoot("wake", i, bt)
		}
		dir := filepath.Join(b.cfg.RunDir, "ckpt-bench", strconv.Itoa(i))
		t0 := time.Now()
		next, err := v.Snapshot(ctx, dir)
		if err != nil {
			fatal(err)
		}
		v.Stop()
		b.record(row{Phase: "hibernate", Iter: i, VM: v.ID, Latency: time.Since(t0)})
		if prevDir != "" {
			os.RemoveAll(prevDir)
		}
		snap, prevDir = next, dir
	}
	os.RemoveAll(filepath.Join(b.cfg.RunDir, "ckpt-bench"))
}

func stopAll(vms []*vm.VM) {
	var wg sync.WaitGroup
	for _, v := range vms {
		if v != nil {
			wg.Add(1)
			go func() { defer wg.Done(); v.Stop() }()
		}
	}
	wg.Wait()
}

func (b *bench) writeRaw(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"phase", "fork_n", "iter", "vm", "latency_ms",
		"rss_kib", "pss_kib", "shared_kib", "private_kib", "private_dirty_kib", "rootfs_mode", "mem_mib", "vcpus"})
	for _, r := range b.rows {
		rec := []string{r.Phase, itoa(r.ForkN), itoa(r.Iter), r.VM, fmt.Sprintf("%.3f", ms(r.Latency))}
		if r.HasMem {
			rec = append(rec, i64(r.Mem.RSS), i64(r.Mem.PSS), i64(r.Mem.Shared()), i64(r.Mem.Private()), i64(r.Mem.PrivateDirty))
		} else {
			rec = append(rec, "", "", "", "", "")
		}
		rec = append(rec, string(b.cfg.RootfsMode), i64(b.cfg.MemMiB), i64(b.cfg.VCPUs))
		w.Write(rec)
	}
	w.Flush()
	return w.Error()
}

func (b *bench) writeSummary(path string) error {
	type key struct {
		phase string
		n     int
	}
	groups := map[key][]row{}
	var order []key
	for _, r := range b.rows {
		k := key{r.Phase, r.ForkN}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"phase", "fork_n", "samples", "p50_ms", "p90_ms", "p99_ms", "min_ms", "max_ms",
		"mean_rss_kib", "mean_pss_kib", "mean_private_kib"})
	for _, k := range order {
		rs := groups[k]
		lat := make([]float64, len(rs))
		var rss, pss, priv, nm int64
		for i, r := range rs {
			lat[i] = ms(r.Latency)
			if r.HasMem {
				rss, pss, priv, nm = rss+r.Mem.RSS, pss+r.Mem.PSS, priv+r.Mem.Private(), nm+1
			}
		}
		sort.Float64s(lat)
		rec := []string{k.phase, itoa(k.n), itoa(len(lat)),
			f3(pct(lat, 50)), f3(pct(lat, 90)), f3(pct(lat, 99)), f3(lat[0]), f3(lat[len(lat)-1])}
		if nm > 0 {
			rec = append(rec, i64(rss/nm), i64(pss/nm), i64(priv/nm))
		} else {
			rec = append(rec, "", "", "")
		}
		w.Write(rec)
	}
	w.Flush()
	return w.Error()
}

func (b *bench) writeMem(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"phase", "fork_n", "rep", "memavailable_delta_per_vm_kib"})
	for _, m := range b.memRows {
		w.Write([]string{m.Phase, itoa(m.ForkN), itoa(m.Rep), i64(m.MemAvailDeltaPerVM)})
	}
	w.Flush()
	return w.Error()
}

// writeMeta records what the numbers were measured on. A latency number
// without the host kernel, CPU and Firecracker version is not a result.
func writeMeta(path string, b *bench, warm string) error {
	fcVersion, _ := exec.Command(b.cfg.FirecrackerBin, "--version").Output()
	osrel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	var snapSize int64
	if st, err := os.Stat(b.snap.MemPath); err == nil {
		snapSize = st.Size()
	}
	meta := map[string]any{
		"time":              time.Now().UTC().Format(time.RFC3339),
		"host_kernel":       strings.TrimSpace(string(osrel)),
		"cpu":               cpuModel(),
		"host_mem_total":    meminfo("MemTotal:"),
		"firecracker":       strings.TrimSpace(strings.SplitN(string(fcVersion), "\n", 2)[0]),
		"go":                runtime.Version(),
		"rootfs_mode":       b.cfg.RootfsMode,
		"guest_mem_mib":     b.cfg.MemMiB,
		"guest_vcpus":       b.cfg.VCPUs,
		"kernel":            b.cfg.KernelPath,
		"rootfs":            b.cfg.RootfsPath,
		"snapshot_mem_size": snapSize,
		"warm_cmd":          warm,
		"work_cmd":          b.work,
		"iters":             b.iters,
		"fork_ns":           b.forkNs,
		"fork_reps":         b.forkReps,
		"fork_parallel":     b.forkPar,
		"drop_caches":       b.dropCaches,
		"settle":            b.settle.String(),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func cpuModel() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "model name") {
			_, v, _ := strings.Cut(l, ":")
			return strings.TrimSpace(v)
		}
	}
	return runtime.GOARCH
}

func meminfo(key string) string {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key) {
			return strings.TrimSpace(strings.TrimPrefix(l, key))
		}
	}
	return ""
}

// pct is the nearest-rank percentile of sorted xs.
func pct(xs []float64, p float64) float64 {
	i := int(math.Ceil(p/100*float64(len(xs)))) - 1
	return xs[max(0, min(i, len(xs)-1))]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
func f3(x float64) string        { return fmt.Sprintf("%.3f", x) }
func itoa(i int) string          { return strconv.Itoa(i) }
func i64(i int64) string         { return strconv.FormatInt(i, 10) }

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

var (
	cur             *bench
	curOut, curWarm string
)

// fatal keeps whatever was measured before the failure: a 30-minute run that
// dies in its last phase should still leave CSVs behind.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	if cur != nil && curOut != "" {
		if werr := cur.writeAll(curOut, curWarm); werr == nil {
			fmt.Fprintf(os.Stderr, "bench: partial results written to %s\n", curOut)
		}
	}
	os.Exit(1)
}
