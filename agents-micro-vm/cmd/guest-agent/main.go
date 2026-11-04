//go:build linux

// guest-agent runs inside the microVM as PID 1. It does three things:
//
//  1. Minimal init: mounts, loopback, zombie reaping.
//  2. Serves exec/ping/status requests from the host over vsock.
//  3. Forwards 127.0.0.1:3128 to the host egress proxy over vsock. The guest
//     has no NIC; this listener is its only route out.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"agents-micro-vm/internal/proto"
)

const hostCID = 2

var (
	inFlight     atomic.Int64
	inFlightPgid sync.Map // pgid -> struct{}; every exec runs in its own group
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("guest-agent: ")

	if os.Getpid() == 1 {
		initSystem()
		// PID 1 inherits every orphan, and reaping with wait4(-1) would steal
		// exit statuses from exec.Cmd.Wait in the server. So PID 1 only reaps,
		// and the server runs as a child.
		self, _ := os.Executable()
		srv := exec.Command(self, "serve")
		srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
		if err := srv.Start(); err != nil {
			log.Fatalf("start server: %v", err)
		}
		reapForever()
	}
	serve()
}

func initSystem() {
	mounts := []struct{ src, dst, fstype, data string }{
		{"proc", "/proc", "proc", ""},
		{"sysfs", "/sys", "sysfs", ""},
		{"devtmpfs", "/dev", "devtmpfs", ""},
		{"tmpfs", "/tmp", "tmpfs", "mode=1777"},
		{"tmpfs", "/run", "tmpfs", "mode=755"},
		// The rootfs is read-only and shared by every VM; agent writes land
		// here, in guest RAM, and so in this VM's private pages only.
		{"tmpfs", "/work", "tmpfs", "mode=777"},
	}
	for _, m := range mounts {
		if err := unix.Mount(m.src, m.dst, m.fstype, 0, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			log.Printf("mount %s: %v", m.dst, err)
		}
	}
	if err := loopbackUp(); err != nil {
		log.Printf("lo up: %v", err)
	}
	_ = unix.Sethostname([]byte("sandbox"))
}

// loopbackUp is `ip link set lo up` without iproute2. The kernel assigns
// 127.0.0.1/8 to lo when it comes up.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func reapForever() {
	for {
		var ws unix.WaitStatus
		if _, err := unix.Wait4(-1, &ws, 0, nil); errors.Is(err, unix.ECHILD) {
			time.Sleep(time.Second)
		}
	}
}

func serve() {
	go egressForwarder()

	l, err := vsock.Listen(proto.AgentPort, nil)
	if err != nil {
		log.Fatalf("vsock listen: %v", err)
	}
	log.Printf("listening on vsock port %d", proto.AgentPort)
	for {
		c, err := l.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(c)
	}
}

func handle(c net.Conn) {
	defer c.Close()
	var req proto.Request
	if err := proto.Read(bufio.NewReader(c), &req); err != nil {
		_ = proto.Write(c, proto.Response{Error: "bad request: " + err.Error()})
		return
	}
	if req.HostUnixNano > 0 {
		syncClock(req.HostUnixNano)
	}

	var resp proto.Response
	switch req.Op {
	case proto.OpPing:
		resp.OK = true
	case proto.OpStatus:
		resp.OK = true
	case proto.OpExec:
		resp = runCommand(req)
	default:
		resp.Error = "unknown op " + string(req.Op)
	}
	resp.InFlight = int(inFlight.Load())
	resp.Background = countBackground()
	_ = proto.Write(c, resp)
}

func runCommand(req proto.Request) proto.Response {
	inFlight.Add(1)
	defer inFlight.Add(-1)

	timeout := 60 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Cmd)
	cmd.Dir = "/work"
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/work",
		"http_proxy=http://" + proto.GuestProxyAddr,
		"HTTP_PROXY=http://" + proto.GuestProxyAddr,
		"https_proxy=http://" + proto.GuestProxyAddr,
		"HTTPS_PROXY=http://" + proto.GuestProxyAddr,
	}
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// `cmd &` leaves a child holding our stdout pipe. Don't wait for it;
	// it shows up in Background instead.
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	err := cmd.Start()
	if err == nil {
		inFlightPgid.Store(cmd.Process.Pid, struct{}{})
		err = cmd.Wait()
		inFlightPgid.Delete(cmd.Process.Pid)
	}
	if cmd.ProcessState == nil { // never started
		return proto.Response{Error: err.Error(), ExitCode: -1}
	}
	resp := proto.Response{
		OK:         true,
		ExitCode:   cmd.ProcessState.ExitCode(),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err != nil && !errors.As(err, new(*exec.ExitError)) && !errors.Is(err, exec.ErrWaitDelay) {
		resp.Error = err.Error()
	}
	return resp
}

// countBackground counts live user processes outside any in-flight exec's
// process group: what `cmd &` left behind. Kernel threads have an empty
// cmdline, and kernel-spawned helpers (bpfilter_umh and friends) are children
// of kthreadd; PID 1 and this server are ours.
func countBackground() int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	n := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || pid == 1 {
			continue
		}
		if cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err != nil || len(cmdline) == 0 {
			continue
		}
		ppid, pgid := parentAndGroup(e.Name())
		if ppid == 2 {
			continue
		}
		if _, busy := inFlightPgid.Load(pgid); busy {
			continue
		}
		n++
	}
	return n
}

// parentAndGroup reads fields 4 and 5 of /proc/<pid>/stat. comm (field 2)
// may contain spaces and parens, so split after the last ')'.
func parentAndGroup(pid string) (ppid, pgid int) {
	b, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return -1, -1
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:]) // state ppid pgrp ...
	if len(f) < 3 {
		return -1, -1
	}
	ppid, _ = strconv.Atoi(f[1])
	pgid, _ = strconv.Atoi(f[2])
	return ppid, pgid
}

func syncClock(hostNano int64) {
	drift := time.Since(time.Unix(0, hostNano))
	if drift > -time.Second && drift < time.Second {
		return
	}
	tv := unix.NsecToTimeval(hostNano)
	if err := unix.Settimeofday(&tv); err != nil {
		log.Printf("settimeofday: %v", err)
	}
}

func egressForwarder() {
	l, err := net.Listen("tcp", proto.GuestProxyAddr)
	if err != nil {
		log.Printf("egress listen: %v", err)
		return
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer c.Close()
			up, err := vsock.Dial(hostCID, proto.EgressPort, nil)
			if err != nil {
				log.Printf("egress dial host: %v", err)
				return
			}
			defer up.Close()
			go func() { _, _ = io.Copy(up, c); _ = up.CloseWrite() }()
			_, _ = io.Copy(c, up)
		}()
	}
}

type capped struct {
	bytes.Buffer
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := proto.MaxOutput - c.Len(); room < len(p) {
		c.truncated = true
		if room > 0 {
			c.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

func (c *capped) String() string {
	if c.truncated {
		return c.Buffer.String() + "\n[truncated]"
	}
	return c.Buffer.String()
}
