// Package idle implements idle-aware checkpointing for one agent session.
//
// The signal is the one Google describes: the host dispatches every tool
// call, so it knows when one finishes. If nothing is in flight and the guest
// reports no leftover background processes, the session is idle. After
// IdleAfter of that, it is either paused (vCPUs stopped, memory kept) or
// hibernated (snapshot to disk, VMM process gone). The next Exec wakes it.
package idle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agents-micro-vm/internal/proto"
	"agents-micro-vm/internal/vm"
)

type Mode string

const (
	ModePause     Mode = "pause"
	ModeHibernate Mode = "hibernate"
)

type State string

const (
	Running    State = "running"
	Paused     State = "paused"
	Hibernated State = "hibernated"
)

type Session struct {
	ID        string
	Cfg       vm.Config   // Cfg.PreStart is where callers attach the egress proxy
	Base      vm.Snapshot // warm template to start from
	IdleAfter time.Duration
	Mode      Mode
	// OnTransition is called after every state change with how long it took.
	OnTransition func(id string, from, to State, took time.Duration)

	mu      sync.Mutex
	v       *vm.VM
	state   State
	snap    vm.Snapshot // what the current VM was restored from
	ownSnap bool        // snap is our checkpoint (deletable), not Base
	gen     int
	busy    int
	timer   *time.Timer
}

// Exec runs cmd in the session's VM, waking it first if needed.
func (s *Session) Exec(ctx context.Context, cmd string, timeout time.Duration) (proto.Response, error) {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	if err := s.wakeLocked(ctx); err != nil {
		s.mu.Unlock()
		return proto.Response{}, err
	}
	s.busy++
	v := s.v
	s.mu.Unlock()

	resp, err := v.Exec(ctx, cmd, timeout)

	s.mu.Lock()
	s.busy--
	if s.busy == 0 {
		s.armLocked(resp.Background)
	}
	s.mu.Unlock()
	return resp, err
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// armLocked starts the idle countdown, or, if the last call left background
// processes running, re-checks until they're gone. A `npm install &` must
// not get frozen mid-download.
func (s *Session) armLocked(background int) {
	wait := s.IdleAfter
	if background > 0 {
		wait = time.Second
	}
	s.timer = time.AfterFunc(wait, func() { s.maybeSleep(background > 0) })
}

func (s *Session) maybeSleep(recheck bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy > 0 || s.state != Running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := s.v.Status(ctx)
	if err != nil || st.InFlight > 0 || st.Background > 0 {
		s.armLocked(1)
		return
	}
	if recheck { // background work just ended; now start the real countdown
		s.armLocked(0)
		return
	}
	if err := s.sleepLocked(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: sleep failed: %v\n", s.ID, err)
	}
}

func (s *Session) sleepLocked(ctx context.Context) error {
	start := time.Now()
	switch s.Mode {
	case ModePause:
		if err := s.v.Pause(ctx); err != nil {
			return err
		}
		s.transition(Paused, start)
	case ModeHibernate:
		s.gen++
		dir := filepath.Join(s.Cfg.RunDir, "ckpt", s.ID, fmt.Sprintf("gen-%d", s.gen))
		snap, err := s.v.Snapshot(ctx, dir)
		if err != nil {
			return err
		}
		_ = s.v.Stop()
		// The old checkpoint backed the VM we just stopped; now it's garbage.
		if s.ownSnap {
			_ = os.RemoveAll(filepath.Dir(s.snap.MemPath))
		}
		s.v, s.snap, s.ownSnap = nil, snap, true
		s.transition(Hibernated, start)
	}
	return nil
}

func (s *Session) wakeLocked(ctx context.Context) error {
	start := time.Now()
	switch s.state {
	case Running:
		return nil
	case Paused:
		if err := s.v.Resume(ctx); err != nil {
			return err
		}
	case "", Hibernated:
		snap := s.snap
		if s.state == "" {
			snap = s.Base
		}
		v, err := vm.Restore(s.Cfg, snap, s.ID)
		if err != nil {
			return err
		}
		if err := v.WaitReady(ctx); err != nil {
			_ = v.Stop()
			return err
		}
		s.v, s.snap = v, snap
	}
	s.transition(Running, start)
	return nil
}

func (s *Session) transition(to State, start time.Time) {
	from := s.state
	s.state = to
	if s.OnTransition != nil {
		s.OnTransition(s.ID, from, to, time.Since(start))
	}
}

// Close stops the VM and deletes any checkpoint this session owns.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.v != nil {
		_ = s.v.Stop()
	}
	if s.ownSnap {
		_ = os.RemoveAll(filepath.Join(s.Cfg.RunDir, "ckpt", s.ID))
	}
}
