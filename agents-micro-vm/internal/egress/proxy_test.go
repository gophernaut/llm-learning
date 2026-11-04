package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// newTestProxy points every upstream dial at one TLS test server, which
// echoes the Authorization header it received.
func newTestProxy(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Host+"|"+r.Header.Get("Authorization"))
	}))
	t.Cleanup(upstream.Close)

	tr := upstream.Client().Transport.(*http.Transport).Clone()
	tr.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return tls.Dial(network, upstream.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	}
	p := &Proxy{
		VMID: "vm-test",
		Rules: map[string]Rule{
			"api.example.com": {Host: "api.example.com", Token: staticToken("s3cret")},
			"pypi.org":        {Host: "pypi.org"},
		},
		Transport: tr,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv
}

func viaProxy(t *testing.T, proxy *httptest.Server, target string, hdr http.Header) (int, string) {
	t.Helper()
	pu, _ := url.Parse(proxy.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", target, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestInjectsBearerAndUpgradesToTLS(t *testing.T) {
	proxy := newTestProxy(t)
	code, body := viaProxy(t, proxy, "http://api.example.com/v1/things", nil)
	if code != 200 || body != "api.example.com|Bearer s3cret" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestGuestAuthHeaderIsReplaced(t *testing.T) {
	proxy := newTestProxy(t)
	_, body := viaProxy(t, proxy, "http://api.example.com/", http.Header{"Authorization": {"Bearer stolen"}})
	if !strings.HasSuffix(body, "Bearer s3cret") {
		t.Fatalf("guest header leaked upstream: %q", body)
	}
}

func TestNoCredentialForPassthroughHost(t *testing.T) {
	proxy := newTestProxy(t)
	_, body := viaProxy(t, proxy, "http://pypi.org/simple/", http.Header{"Authorization": {"Bearer mine"}})
	if body != "pypi.org|" {
		t.Fatalf("got %q", body)
	}
}

func TestDeniesUnlistedHost(t *testing.T) {
	proxy := newTestProxy(t)
	if code, _ := viaProxy(t, proxy, "http://evil.example.net/", nil); code != http.StatusForbidden {
		t.Fatalf("got %d", code)
	}
}

func connect(t *testing.T, proxy *httptest.Server, target string) string {
	t.Helper()
	c, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	return line
}

func TestDeniesConnectToCredentialedHost(t *testing.T) {
	proxy := newTestProxy(t)
	if line := connect(t, proxy, "api.example.com:443"); !strings.Contains(line, "403") {
		t.Fatalf("got %q", line)
	}
}

func TestDeniesConnectToNon443(t *testing.T) {
	proxy := newTestProxy(t)
	if line := connect(t, proxy, "pypi.org:22"); !strings.Contains(line, "403") {
		t.Fatalf("got %q", line)
	}
}

func TestParseRule(t *testing.T) {
	t.Setenv("TOK", "abc")
	r, err := ParseRule("API.Example.com=env:TOK", time.Minute)
	if err != nil || r.Host != "api.example.com" {
		t.Fatal(r, err)
	}
	if tok, _ := r.Token.Token(context.Background()); tok != "abc" {
		t.Fatalf("got %q", tok)
	}
	if _, err := ParseRule("x=bogus:1", time.Minute); err == nil {
		t.Fatal("want error")
	}
}
