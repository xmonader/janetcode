package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// upstream is a plain HTTP server standing in for the internet; the
// proxy's dialer sends every host to it, so the test needs no network.
func proxyFor(t *testing.T, pol *Policy, ask func(string) bool) (*Proxy, *Recorder) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s", r.Host)
	}))
	t.Cleanup(up.Close)
	rec := &Recorder{}
	rec.Begin(1)
	p := &Proxy{Policy: pol, Recorder: func() *Recorder { return rec }, Ask: ask,
		Dial: func(_ context.Context, network, _ string) (net.Conn, error) {
			return net.Dial(network, up.Listener.Addr().String())
		}}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, rec
}

func get(t *testing.T, p *Proxy, target string) (int, string) {
	t.Helper()
	pu, _ := url.Parse("http://" + p.Addr())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func connect(t *testing.T, p *Proxy, hostport string) string {
	t.Helper()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", hostport, hostport)
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	return strings.TrimSpace(line)
}

func TestProxyEnforcesThePolicy(t *testing.T) {
	pol := &Policy{Default: Block, Allow: []string{"example.org"}}
	p, rec := proxyFor(t, pol, nil)
	if code, body := get(t, p, "http://example.org/x"); code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("allowed host: %d %q", code, body)
	}
	if code, body := get(t, p, "http://example.com/"); code != 403 || !strings.Contains(body, "egress") {
		t.Fatalf("blocked host: %d %q", code, body)
	}
	if line := connect(t, p, "example.org:443"); !strings.Contains(line, "200") {
		t.Fatalf("CONNECT allowed host: %q", line)
	}
	if line := connect(t, p, "example.com:443"); !strings.Contains(line, "403") {
		t.Fatalf("CONNECT blocked host: %q", line)
	}
	rep := rec.Current()
	var blocked, passed bool
	for _, e := range rep.Entries {
		if e.Host == "example.com" && e.Blocked {
			blocked = true
		}
		if e.Host == "example.org" && !e.Blocked && e.Kind == KindCommand {
			passed = true
		}
	}
	if !blocked || !passed {
		t.Fatalf("report does not show both hosts: %+v", rep.Entries)
	}
}

func TestProxyAsksOnceAndRemembersYes(t *testing.T) {
	asked := 0
	p, _ := proxyFor(t, &Policy{Default: Ask}, func(h string) bool { asked++; return h == "yes.example" })
	for i := 0; i < 2; i++ {
		if code, _ := get(t, p, "http://yes.example/"); code != 200 {
			t.Fatalf("approved host refused: %d", code)
		}
	}
	if code, _ := get(t, p, "http://no.example/"); code != 403 {
		t.Fatalf("refused host passed: %d", code)
	}
	if asked != 2 {
		t.Fatalf("asked %d times, want 2 (the yes remembered)", asked)
	}
}

func TestProxyAskWithNoOneToAskRefuses(t *testing.T) {
	p, _ := proxyFor(t, &Policy{Default: Ask}, nil)
	if code, _ := get(t, p, "http://x.example/"); code != 403 {
		t.Fatalf("got %d, want 403", code)
	}
}

// TestProxyServesOnItsUnixSocket is the way a command in a network
// namespace of its own reaches the proxy: the same filtering, over a
// socket file, which Close takes away.
func TestProxyServesOnItsUnixSocket(t *testing.T) {
	p, _ := proxyFor(t, &Policy{Default: Block, Allow: []string{"ok.test"}}, nil)
	sock, err := p.UnixSocket()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := p.UnixSocket(); again != sock {
		t.Fatalf("second call opened another socket: %s", again)
	}
	c := &http.Client{Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.invalid") },
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	get := func(target string) (int, string) {
		resp, err := c.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("http://ok.test/"); code != 200 || !strings.Contains(body, "hello from ok.test") {
		t.Fatalf("allowed host over the socket: %d %q", code, body)
	}
	if code, _ := get("http://evil.test/"); code != http.StatusForbidden {
		t.Fatalf("a host the policy refuses got %d over the socket", code)
	}
	p.Close()
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket file left behind after Close")
	}
}
