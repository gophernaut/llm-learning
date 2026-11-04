// Package egress is the host-side proxy at the far end of the guest's vsock
// egress port. One Proxy per VM; it listens on that VM's
// "<uds_path>_<port>" socket, so the socket a request arrives on *is* the
// sandbox identity. Nothing the guest sends can change which VM it is.
//
// Policy:
//   - Host not on the allowlist: 403.
//   - Allowlisted, no credential: CONNECT is tunnelled as opaque TLS.
//   - Allowlisted with a credential: the guest must send plain HTTP
//     ("http://api.example.com/..."). The proxy drops any auth headers the
//     guest set, adds "Authorization: Bearer <token>", and sends the request
//     upstream over HTTPS. The token exists only in this process.
//
// Injecting into a CONNECT tunnel would need TLS interception with a CA the
// guest trusts. That's what production gateways do; plain-HTTP-to-the-proxy
// gets the same property (the guest never holds the secret) without it.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TokenSource returns the credential to attach to an upstream request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type Rule struct {
	Host  string      // exact hostname, no port
	Token TokenSource // nil: allowed without a credential
}

type Proxy struct {
	VMID      string
	Rules     map[string]Rule
	Transport http.RoundTripper // upstream; defaults to http.DefaultTransport
	Log       *slog.Logger

	rp *httputil.ReverseProxy
	mu sync.Once
}

type tokenKey struct{}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Do(p.init)
	host, port := r.URL.Hostname(), ""
	if r.Method == http.MethodConnect {
		host, port, _ = net.SplitHostPort(r.Host)
	}
	rule, ok := p.Rules[strings.ToLower(host)]
	log := p.Log.With("vm", p.VMID, "method", r.Method, "host", host)

	switch {
	case !ok:
		log.Warn("egress denied", "reason", "not allowlisted")
		http.Error(w, "egress to "+host+" is not allowed", http.StatusForbidden)
	case r.Method == http.MethodConnect && port != "443":
		log.Warn("egress denied", "reason", "CONNECT to port "+port)
		http.Error(w, "CONNECT is only allowed to port 443", http.StatusForbidden)
	case r.Method == http.MethodConnect && rule.Token != nil:
		log.Warn("egress denied", "reason", "credentialed host over CONNECT")
		http.Error(w, "use http://"+host+"/... ; the proxy adds TLS and credentials", http.StatusForbidden)
	case r.Method == http.MethodConnect:
		log.Info("egress tunnel")
		p.tunnel(w, r)
	case r.URL.Host == "":
		http.Error(w, "proxy expects absolute-form requests", http.StatusBadRequest)
	default:
		ctx := r.Context()
		if rule.Token != nil {
			tok, err := rule.Token.Token(ctx)
			if err != nil {
				log.Error("token fetch failed", "err", err)
				http.Error(w, "credential unavailable", http.StatusBadGateway)
				return
			}
			ctx = context.WithValue(ctx, tokenKey{}, tok)
		}
		log.Info("egress request", "path", r.URL.Path, "credential", rule.Token != nil)
		p.rp.ServeHTTP(w, r.WithContext(ctx))
	}
}

func (p *Proxy) init() {
	if p.Log == nil {
		p.Log = slog.Default()
	}
	p.rp = &httputil.ReverseProxy{
		Transport: p.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = pr.In.URL.Hostname()
			pr.Out.Host = pr.Out.URL.Host
			// Whatever the guest put here is either junk or an attempt to
			// smuggle its own credential; neither goes upstream.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Proxy-Authorization")
			if tok, ok := pr.In.Context().Value(tokenKey{}).(string); ok {
				pr.Out.Header.Set("Authorization", "Bearer "+tok)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.Log.Error("upstream error", "vm", p.VMID, "host", r.URL.Host, "err", err)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	up, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, "dial upstream", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() {
		defer up.Close()
		_, _ = buf.Reader.WriteTo(up) // bytes the client sent after the CONNECT line
		_, _ = io.Copy(up, down)
	}()
	defer down.Close()
	_, _ = io.Copy(down, up)
}

// Serve accepts on l until it's closed.
func (p *Proxy) Serve(l net.Listener) error {
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	err := srv.Serve(l)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// ListenUnix serves p on a unix socket (a VM's egress port path) in the
// background and returns a function that closes it.
func (p *Proxy) ListenUnix(path string) (func(), error) {
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() { _ = p.Serve(l) }()
	return func() { _ = l.Close() }, nil
}

// CachedToken calls Fetch at most once per TTL. Short TTLs are the point:
// a token that leaks from the proxy's memory is useless in minutes.
type CachedToken struct {
	Fetch func(ctx context.Context) (string, error)
	TTL   time.Duration

	mu      sync.Mutex
	tok     string
	expires time.Time
}

func (c *CachedToken) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && time.Now().Before(c.expires) {
		return c.tok, nil
	}
	tok, err := c.Fetch(ctx)
	if err != nil {
		return "", err
	}
	c.tok, c.expires = strings.TrimSpace(tok), time.Now().Add(c.TTL)
	return c.tok, nil
}

// ParseRule turns a flag value into a Rule:
//
//	pypi.org                                  allow, no credential
//	api.github.com=exec:gh auth token          run a command for the token
//	api.example.com=file:/run/tokens/example   read a file the minting service rotates
//	api.example.com=env:EXAMPLE_TOKEN          read the proxy's own environment
func ParseRule(spec string, ttl time.Duration) (Rule, error) {
	host, src, hasSrc := strings.Cut(spec, "=")
	r := Rule{Host: strings.ToLower(host)}
	if !hasSrc {
		return r, nil
	}
	kind, arg, _ := strings.Cut(src, ":")
	var fetch func(ctx context.Context) (string, error)
	switch kind {
	case "exec":
		fetch = func(ctx context.Context) (string, error) {
			out, err := exec.CommandContext(ctx, "sh", "-c", arg).Output()
			return string(out), err
		}
	case "file":
		fetch = func(context.Context) (string, error) {
			b, err := os.ReadFile(arg)
			return string(b), err
		}
	case "env":
		fetch = func(context.Context) (string, error) {
			if v := os.Getenv(arg); v != "" {
				return v, nil
			}
			return "", fmt.Errorf("$%s is empty", arg)
		}
	default:
		return r, fmt.Errorf("rule %q: unknown token source %q", spec, kind)
	}
	r.Token = &CachedToken{Fetch: fetch, TTL: ttl}
	return r, nil
}
