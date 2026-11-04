// orchestrator: build a warm snapshot, fork it N times, or run an
// idle-checkpointed session.
//
//	orchestrator template [-warm cmd]          cold boot, warm up, snapshot
//	orchestrator fork -n 8 -cmd 'uname -a'     N forks of the snapshot run cmd
//	orchestrator session -idle 3s -mode hibernate   read commands from stdin
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"agents-micro-vm/internal/cliflags"
	"agents-micro-vm/internal/idle"
	"agents-micro-vm/internal/vm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orchestrator template|fork|session [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "template":
		err = template(os.Args[2:])
	case "fork":
		err = fork(os.Args[2:])
	case "session":
		err = session(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func template(args []string) error {
	fs := flag.NewFlagSet("template", flag.ExitOnError)
	vf := cliflags.RegisterVM(fs)
	warm := fs.String("warm", "", "command to run before snapshotting, e.g. to pre-import libraries")
	fs.Parse(args)
	cfg, err := vf.Config()
	if err != nil {
		return err
	}
	ctx := context.Background()

	t0 := time.Now()
	v, err := vm.ColdBoot(cfg, "template")
	if err != nil {
		return err
	}
	defer v.Stop()
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := v.WaitReady(rctx); err != nil {
		return err
	}
	fmt.Printf("cold boot to agent ready: %v\n", time.Since(t0).Round(time.Microsecond))

	if *warm != "" {
		resp, err := v.Exec(ctx, *warm, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("warm-up: %w", err)
		}
		fmt.Printf("warm-up exit=%d in %dms\n", resp.ExitCode, resp.DurationMs)
	}

	t1 := time.Now()
	snap, err := v.Snapshot(ctx, vf.SnapDir)
	if err != nil {
		return err
	}
	st, _ := os.Stat(snap.MemPath)
	fmt.Printf("snapshot written in %v: %s (%d MiB)\n", time.Since(t1).Round(time.Millisecond), vf.SnapDir, st.Size()>>20)
	return nil
}

func fork(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ExitOnError)
	vf := cliflags.RegisterVM(fs)
	ef := cliflags.RegisterEgress(fs)
	n := fs.Int("n", 4, "number of forks")
	cmd := fs.String("cmd", "echo $(hostname) $(head -c8 /dev/urandom | od -An -tx1 | tr -d ' ')", "command each fork runs")
	keep := fs.Bool("keep", false, "leave forks running until Enter is pressed")
	fs.Parse(args)
	cfg, err := vf.Config()
	if err != nil {
		return err
	}
	if cfg.PreStart, err = ef.PreStart(); err != nil {
		return err
	}
	snap := vf.Snapshot()
	ctx := context.Background()

	type result struct {
		id    string
		ready time.Duration
		out   string
		err   error
		vm    *vm.VM
	}
	results := make([]result, *n)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := range *n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &results[i]
			r.id = fmt.Sprintf("fork-%03d", i)
			start := time.Now()
			v, err := vm.Restore(cfg, snap, r.id)
			if err != nil {
				r.err = err
				return
			}
			r.vm = v
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if r.err = v.WaitReady(rctx); r.err != nil {
				return
			}
			r.ready = time.Since(start)
			resp, err := v.Exec(ctx, *cmd, time.Minute)
			r.out, r.err = strings.TrimSpace(resp.Stdout+resp.Stderr), err
		}()
	}
	wg.Wait()
	fmt.Printf("%d forks ready+ran in %v\n", *n, time.Since(t0).Round(time.Millisecond))
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("%s  ERROR %v\n", r.id, r.err)
			continue
		}
		fmt.Printf("%s  ready=%-10v %s\n", r.id, r.ready.Round(time.Microsecond), r.out)
	}
	if *keep {
		fmt.Print("forks running; inspect with ps/smem, press Enter to stop ")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	for _, r := range results {
		if r.vm != nil {
			r.vm.Stop()
		}
	}
	return nil
}

func session(args []string) error {
	fs := flag.NewFlagSet("session", flag.ExitOnError)
	vf := cliflags.RegisterVM(fs)
	ef := cliflags.RegisterEgress(fs)
	idleAfter := fs.Duration("idle", 3*time.Second, "idle time before checkpointing")
	mode := fs.String("mode", "hibernate", "pause | hibernate")
	fs.Parse(args)
	cfg, err := vf.Config()
	if err != nil {
		return err
	}
	if cfg.PreStart, err = ef.PreStart(); err != nil {
		return err
	}
	s := &idle.Session{
		ID:        "session",
		Cfg:       cfg,
		Base:      vf.Snapshot(),
		IdleAfter: *idleAfter,
		Mode:      idle.Mode(*mode),
		OnTransition: func(id string, from, to idle.State, took time.Duration) {
			fmt.Fprintf(os.Stderr, "[%s] %s -> %s in %v\n", id, orNew(from), to, took.Round(time.Microsecond))
		},
	}
	defer s.Close()

	fmt.Fprintln(os.Stderr, "one shell command per line; Ctrl-D to quit")
	sc := bufio.NewScanner(os.Stdin)
	for fmt.Fprint(os.Stderr, "> "); sc.Scan(); fmt.Fprint(os.Stderr, "> ") {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t0 := time.Now()
		resp, err := s.Exec(context.Background(), line, 5*time.Minute)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Print(resp.Stdout)
		fmt.Fprint(os.Stderr, resp.Stderr)
		fmt.Fprintf(os.Stderr, "(exit=%d, guest %dms, total %v, background=%d)\n",
			resp.ExitCode, resp.DurationMs, time.Since(t0).Round(time.Millisecond), resp.Background)
	}
	return nil
}

func orNew(s idle.State) idle.State {
	if s == "" {
		return "new"
	}
	return s
}
