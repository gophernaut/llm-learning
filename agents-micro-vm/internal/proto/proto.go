// Package proto is the host<->guest wire format. One request per vsock
// connection: the host writes a JSON line, the guest writes a JSON line back
// and closes. A connection per call keeps us correct across snapshot/restore,
// because Firecracker closes every open vsock connection on resume.
package proto

import (
	"bufio"
	"encoding/json"
	"io"
)

const (
	// AgentPort is the guest vsock port the guest agent listens on.
	AgentPort = 10000
	// EgressPort is the host vsock port the guest forwards proxy traffic to.
	// Firecracker maps it to the unix socket "<uds_path>_10001" on the host.
	EgressPort = 10001
	// GuestProxyAddr is where software inside the guest finds its HTTP proxy.
	GuestProxyAddr = "127.0.0.1:3128"

	// MaxOutput caps stdout/stderr returned per call.
	MaxOutput = 1 << 20
)

type Op string

const (
	OpPing   Op = "ping"
	OpExec   Op = "exec"
	OpStatus Op = "status"
)

type Request struct {
	Op        Op                `json:"op"`
	Cmd       string            `json:"cmd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMs int64             `json:"timeout_ms,omitempty"`
	// HostUnixNano lets the guest fix its wall clock after a restore: a VM
	// resumed from a snapshot wakes up with the clock it had when paused.
	HostUnixNano int64 `json:"host_unix_nano,omitempty"`
}

type Response struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMs int64  `json:"duration_ms"`

	// Status fields: what the host needs to decide the sandbox is idle.
	InFlight   int `json:"in_flight"`  // exec calls currently running
	Background int `json:"background"` // other user processes still alive
}

func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func Read(r *bufio.Reader, v any) error {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return err
	}
	return json.Unmarshal(line, v)
}
