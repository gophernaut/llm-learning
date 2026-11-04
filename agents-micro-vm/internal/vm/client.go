package vm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agents-micro-vm/internal/proto"
)

// Exec runs a shell command in the guest and waits for it to finish.
func (v *VM) Exec(ctx context.Context, cmd string, timeout time.Duration) (proto.Response, error) {
	return v.call(ctx, proto.Request{Op: proto.OpExec, Cmd: cmd, TimeoutMs: timeout.Milliseconds()})
}

// Status reports in-flight exec calls and leftover background processes.
func (v *VM) Status(ctx context.Context) (proto.Response, error) {
	return v.call(ctx, proto.Request{Op: proto.OpStatus})
}

func (v *VM) Ping(ctx context.Context) (proto.Response, error) {
	return v.call(ctx, proto.Request{Op: proto.OpPing})
}

// call opens a host-initiated vsock connection. Firecracker's protocol: dial
// the VM's unix socket, send "CONNECT <port>\n", read "OK <host_port>\n",
// then the stream is connected to the guest listener on <port>.
func (v *VM) call(ctx context.Context, req proto.Request) (proto.Response, error) {
	var resp proto.Response
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", v.VsockPath)
	if err != nil {
		return resp, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else if req.TimeoutMs > 0 {
		_ = c.SetDeadline(time.Now().Add(time.Duration(req.TimeoutMs)*time.Millisecond + 5*time.Second))
	}

	if _, err := fmt.Fprintf(c, "CONNECT %d\n", proto.AgentPort); err != nil {
		return resp, err
	}
	r := bufio.NewReader(c)
	ack, err := r.ReadString('\n')
	if err != nil {
		return resp, fmt.Errorf("vsock connect: %w", err) // guest not listening yet
	}
	if !strings.HasPrefix(ack, "OK ") {
		return resp, fmt.Errorf("vsock connect: unexpected %q", ack)
	}

	req.HostUnixNano = time.Now().UnixNano()
	if err := proto.Write(c, req); err != nil {
		return resp, err
	}
	if err := proto.Read(r, &resp); err != nil {
		return resp, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("guest: %s", resp.Error)
	}
	return resp, nil
}

// apiPut talks to the Firecracker API socket directly, for endpoints the
// SDK's generated client doesn't model (pmem, vsock_override).
func apiPut(ctx context.Context, sock, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("PUT %s: %s: %s", path, res.Status, bytes.TrimSpace(msg))
	}
	return nil
}
