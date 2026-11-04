// Package cliflags holds the flags shared by the orchestrator and bench.
package cliflags

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agents-micro-vm/internal/egress"
	"agents-micro-vm/internal/proto"
	"agents-micro-vm/internal/vm"
)

type VM struct {
	cfg        vm.Config
	rootfsMode string
	SnapDir    string
}

func RegisterVM(fs *flag.FlagSet) *VM {
	f := &VM{}
	fs.StringVar(&f.cfg.FirecrackerBin, "fc", "firecracker", "firecracker binary")
	fs.StringVar(&f.cfg.KernelPath, "kernel", "images/vmlinux", "guest kernel (uncompressed vmlinux)")
	fs.StringVar(&f.cfg.RootfsPath, "rootfs", "images/rootfs.ext4", "guest rootfs (ext4, contains /usr/local/bin/guest-agent)")
	fs.StringVar(&f.rootfsMode, "rootfs-mode", "block", "block | pmem (pmem mounts the rootfs with DAX)")
	fs.Int64Var(&f.cfg.MemMiB, "mem", 512, "guest memory MiB")
	fs.Int64Var(&f.cfg.VCPUs, "vcpus", 1, "guest vCPUs")
	fs.BoolVar(&f.cfg.Console, "console", false, "enable serial console (slower boot)")
	fs.StringVar(&f.cfg.RunDir, "run-dir", "/tmp/agents-micro-vm", "per-VM sockets and logs")
	fs.BoolVar(&f.cfg.Verbose, "v", false, "SDK debug logging")
	fs.StringVar(&f.SnapDir, "snap", "snapshots/warm", "warm template snapshot directory")
	return f
}

func (f *VM) Config() (vm.Config, error) {
	c := f.cfg
	switch m := vm.RootfsMode(f.rootfsMode); m {
	case vm.RootfsBlock, vm.RootfsPmem:
		c.RootfsMode = m
	default:
		return c, fmt.Errorf("-rootfs-mode: want block or pmem, got %q", f.rootfsMode)
	}
	for _, p := range []*string{&c.KernelPath, &c.RootfsPath} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return c, err
		}
		*p = abs
	}
	return c, os.MkdirAll(c.RunDir, 0o755)
}

func (f *VM) Snapshot() vm.Snapshot {
	abs, _ := filepath.Abs(f.SnapDir)
	return vm.Snapshot{MemPath: filepath.Join(abs, "mem"), StatePath: filepath.Join(abs, "state")}
}

// Egress collects -allow / -inject rules.
type Egress struct {
	specs []string
	ttl   time.Duration
	quiet bool
}

func RegisterEgress(fs *flag.FlagSet) *Egress {
	e := &Egress{}
	fs.Func("allow", "allow egress to `host` without a credential (repeatable)", func(s string) error {
		e.specs = append(e.specs, s)
		return nil
	})
	fs.Func("inject", "allow `host=source` and add a bearer token from source: exec:<cmd> | file:<path> | env:<VAR> (repeatable)", func(s string) error {
		if !strings.Contains(s, "=") {
			return fmt.Errorf("want host=source")
		}
		e.specs = append(e.specs, s)
		return nil
	})
	fs.DurationVar(&e.ttl, "token-ttl", 5*time.Minute, "how long a fetched token is reused")
	fs.BoolVar(&e.quiet, "quiet-egress", false, "don't log egress decisions")
	return e
}

// PreStart returns a vm.Config.PreStart hook that binds a per-VM egress
// proxy on the VM's egress port before Firecracker starts. The listener is
// closed when the VM stops.
func (e *Egress) PreStart() (func(*vm.VM) error, error) {
	rules := map[string]egress.Rule{}
	for _, s := range e.specs {
		r, err := egress.ParseRule(s, e.ttl)
		if err != nil {
			return nil, err
		}
		rules[r.Host] = r
	}
	var out io.Writer = os.Stderr
	if e.quiet {
		out = io.Discard
	}
	log := slog.New(slog.NewTextHandler(out, nil))
	return func(v *vm.VM) error {
		p := &egress.Proxy{VMID: v.ID, Rules: rules, Log: log}
		closeFn, err := p.ListenUnix(v.GuestPortPath(proto.EgressPort))
		if err != nil {
			return err
		}
		v.OnStop(closeFn)
		return nil
	}, nil
}
