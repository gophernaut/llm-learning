// Package vm wraps firecracker-go-sdk with the three operations this project
// needs: cold boot, snapshot, and restore. Restore is also how we fork: N
// restores of one snapshot.
package vm

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
)

type RootfsMode string

const (
	// RootfsBlock attaches the rootfs as a read-only virtio-block device.
	// Each guest has its own page cache copy of the files it reads.
	RootfsBlock RootfsMode = "block"
	// RootfsPmem attaches the rootfs as read-only virtio-pmem, mounted with
	// DAX. The guest reads file pages straight from the host mapping, so the
	// page cache is not duplicated per guest. This is the closest Firecracker
	// analogue to the DAX design Google describes.
	RootfsPmem RootfsMode = "pmem"

	guestCID = 3 // per-VM; Firecracker vsock is UDS-backed, so all VMs can share it
)

type Config struct {
	FirecrackerBin string
	KernelPath     string
	RootfsPath     string
	RootfsMode     RootfsMode
	MemMiB         int64
	VCPUs          int64
	Console        bool   // serial console on; slows boot, useful for debugging
	RunDir         string // per-VM directories go here
	Verbose        bool

	// PreStart runs after the VM's directory exists and before Firecracker
	// starts, e.g. to bind the egress socket before the guest can dial it.
	PreStart func(v *VM) error
}

// Snapshot is a paused VM written to disk. MemPath is mapped MAP_PRIVATE by
// every VM restored from it, so it must not change while any of them run.
type Snapshot struct {
	MemPath   string
	StatePath string
}

type VM struct {
	ID        string
	Dir       string
	VsockPath string // host side of the guest's vsock device
	PID       int

	m      *firecracker.Machine
	cancel context.CancelFunc
	onStop []func()
}

// OnStop registers f to run when the VM is stopped.
func (v *VM) OnStop(f func()) { v.onStop = append(v.onStop, f) }

func (c Config) kernelArgs() string {
	args := "reboot=k panic=1 pci=off init=/usr/local/bin/guest-agent"
	if c.Console {
		args = "console=ttyS0 " + args
	} else {
		args += " 8250.nr_uarts=0 quiet loglevel=0"
	}
	switch c.RootfsMode {
	case RootfsPmem:
		args += " root=/dev/pmem0 rootflags=dax ro"
	default:
		args += " root=/dev/vda ro"
	}
	return args
}

func (c Config) logger() *logrus.Entry {
	l := logrus.New()
	if !c.Verbose {
		l.SetOutput(io.Discard)
	}
	return logrus.NewEntry(l)
}

// ColdBoot boots a fresh VM from kernel + rootfs. It returns once Firecracker
// has started the guest, not when the guest agent is up; call WaitReady.
func ColdBoot(cfg Config, id string) (*VM, error) {
	v, err := newVM(cfg, id)
	if err != nil {
		return nil, err
	}
	fc := firecracker.Config{
		SocketPath:      filepath.Join(v.Dir, "api.sock"),
		KernelImagePath: cfg.KernelPath,
		KernelArgs:      cfg.kernelArgs(),
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(cfg.VCPUs),
			MemSizeMib: firecracker.Int64(cfg.MemMiB),
			Smt:        firecracker.Bool(false),
		},
		VsockDevices: []firecracker.VsockDevice{{ID: "agent", Path: v.VsockPath, CID: guestCID}},
	}
	var opts []firecracker.Opt
	switch cfg.RootfsMode {
	case RootfsPmem:
		// The SDK's generated client predates virtio-pmem, so add the device
		// with a raw API call in the init handler chain, before InstanceStart.
		opts = append(opts, func(m *firecracker.Machine) {
			m.Handlers.FcInit = m.Handlers.FcInit.AppendAfter(firecracker.AttachDrivesHandlerName, firecracker.Handler{
				Name: "agents.AttachPmem",
				Fn: func(ctx context.Context, m *firecracker.Machine) error {
					return apiPut(ctx, m.Cfg.SocketPath, "/pmem/rootfs", map[string]any{
						"id":           "rootfs",
						"path_on_host": cfg.RootfsPath,
						"root_device":  false, // root= is in our kernel args
						"read_only":    true,
					})
				},
			})
		})
	default:
		fc.Drives = []models.Drive{{
			DriveID:      firecracker.String("rootfs"),
			PathOnHost:   firecracker.String(cfg.RootfsPath),
			IsRootDevice: firecracker.Bool(false),
			IsReadOnly:   firecracker.Bool(true),
		}}
	}
	return v, v.start(cfg, fc, opts...)
}

// Restore starts a new Firecracker process and loads snap into it. Every VM
// restored from the same snapshot shares its memory file copy-on-write: pages
// the guest only reads stay shared in the host page cache, pages it writes
// become private anonymous memory of that VM.
func Restore(cfg Config, snap Snapshot, id string) (*VM, error) {
	v, err := newVM(cfg, id)
	if err != nil {
		return nil, err
	}
	fc := firecracker.Config{SocketPath: filepath.Join(v.Dir, "api.sock")}
	opts := []firecracker.Opt{
		firecracker.WithSnapshot(snap.MemPath, snap.StatePath),
		// The snapshot remembers the vsock socket path of the VM it was taken
		// from. N forks can't all bind it, so we replace the SDK's load step
		// with one that sets vsock_override (Firecracker >= 1.16).
		func(m *firecracker.Machine) {
			m.Handlers.FcInit = m.Handlers.FcInit.Swap(firecracker.Handler{
				Name: firecracker.LoadSnapshotHandlerName,
				Fn: func(ctx context.Context, m *firecracker.Machine) error {
					return apiPut(ctx, m.Cfg.SocketPath, "/snapshot/load", map[string]any{
						"snapshot_path":  snap.StatePath,
						"mem_backend":    map[string]string{"backend_type": "File", "backend_path": snap.MemPath},
						"resume_vm":      true,
						"vsock_override": map[string]string{"uds_path": v.VsockPath},
					})
				},
			})
		},
	}
	return v, v.start(cfg, fc, opts...)
}

func newVM(cfg Config, id string) (*VM, error) {
	dir := filepath.Join(cfg.RunDir, id)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &VM{ID: id, Dir: dir, VsockPath: filepath.Join(dir, "v.sock")}, nil
}

// GuestPortPath is where Firecracker delivers guest-initiated vsock
// connections to the given port.
func (v *VM) GuestPortPath(port uint32) string {
	return fmt.Sprintf("%s_%d", v.VsockPath, port)
}

func (v *VM) start(cfg Config, fc firecracker.Config, opts ...firecracker.Opt) error {
	if cfg.PreStart != nil {
		if err := cfg.PreStart(v); err != nil {
			return err
		}
	}
	started := false
	defer func() {
		if !started {
			v.runOnStop()
		}
	}()
	logf, err := os.Create(filepath.Join(v.Dir, "firecracker.log"))
	if err != nil {
		return err
	}
	// The SDK kills the VMM when this context ends, so it lives as long as
	// the VM, not as long as the caller's request.
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(cfg.FirecrackerBin).
		WithSocketPath(fc.SocketPath).
		WithStdout(logf).
		WithStderr(logf).
		Build(ctx)

	opts = append([]firecracker.Opt{
		firecracker.WithProcessRunner(cmd),
		firecracker.WithLogger(cfg.logger()),
	}, opts...)
	fc.VMID = v.ID
	fc.ForwardSignals = []os.Signal{} // don't let Ctrl-C on the orchestrator fan out to N VMMs

	m, err := firecracker.NewMachine(ctx, fc, opts...)
	if err != nil {
		cancel()
		return err
	}
	if err := m.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", v.ID, err)
	}
	v.m = m
	v.PID, _ = m.PID()
	started = true
	return nil
}

func (v *VM) runOnStop() {
	for _, f := range v.onStop {
		f()
	}
	v.onStop = nil
}

// WaitReady polls the guest agent until it answers. On a cold boot this is
// "kernel booted and init ran"; on a restore it's "vCPUs resumed and the
// agent's listener is reachable again".
//
// A guest that isn't listening yet fails fast (Firecracker resets the
// connection), so the per-attempt timeout only covers a guest that accepted
// and is slow to answer. Keep it long: a short one cancels slow first replies
// over and over, and with many forks starting at once that never converges.
func (v *VM) WaitReady(ctx context.Context) error {
	for {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := v.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s not ready: %w (last error: %v)", v.ID, ctx.Err(), err)
		case <-time.After(time.Millisecond):
		}
	}
}

func (v *VM) Pause(ctx context.Context) error  { return v.m.PauseVM(ctx) }
func (v *VM) Resume(ctx context.Context) error { return v.m.ResumeVM(ctx) }

// Snapshot pauses the VM and writes a full snapshot into dir. The VM stays
// paused; the caller decides whether to resume or stop it.
func (v *VM) Snapshot(ctx context.Context, dir string) (Snapshot, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{MemPath: filepath.Join(dir, "mem"), StatePath: filepath.Join(dir, "state")}
	if err := v.m.PauseVM(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("pause: %w", err)
	}
	if err := v.m.CreateSnapshot(ctx, snap.MemPath, snap.StatePath); err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot: %w", err)
	}
	return snap, nil
}

// Stop kills the VMM and removes the VM's run directory.
func (v *VM) Stop() error {
	if v.m == nil {
		v.runOnStop()
		return nil
	}
	err := v.m.StopVMM()
	v.cancel()
	v.runOnStop()
	_ = os.RemoveAll(v.Dir)
	return err
}
