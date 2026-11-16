package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy is the filtering HTTP proxy a sandboxed bash command is pointed
// at when an egress policy is active. The sandbox takes direct network
// access away (Landlock allows TCP connect only to the proxy's port, the
// docker backend puts the container on an internal network whose only
// way out is this listener, seatbelt allows only localhost:port), so the
// policy is enforced for every host a command reaches, not only for the
// hosts a command line happens to spell out.
//
// It speaks CONNECT (https, and any TCP tunnel a client asks for) and
// plain absolute-URI HTTP requests. Each host is decided by Policy:
// allowed hosts pass, blocked ones get a 403 saying why, and an "ask" is
// put to Ask, whose yes is remembered for the session.
type Proxy struct {
	Policy *Policy
	// Recorder receives what went through: one entry per host, with the
	// bytes sent upstream. Read on every connection so a caller can set it
	// after Start; nil records nothing.
	Recorder func() *Recorder
	// Ask decides a host the policy wants asked about. Nil refuses it,
	// as a run with no one to ask would.
	Ask func(host string) bool
	// Dial replaces the upstream dialer (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Canaries, when set, are the honeytokens planted in the commands'
	// environment (see Canary). A request that carries one — in the
	// CONNECT target, a plain request's URL, headers or the first 64 KB of
	// its body — is refused, and OnCanary is told which host it was for.
	// What travels inside a TLS tunnel is not visible here.
	Canaries func() []Canary
	OnCanary func(host string)

	mu        sync.Mutex
	listeners map[string]net.Listener // by listen IP
	addrs     map[string]string
	sockDir   string // private directory holding the unix listener
	sockPath  string
	allowed   map[string]bool // hosts Ask said yes to
	srv       *http.Server
	closed    bool
}

// Start opens the loopback listener. It is idempotent.
func (p *Proxy) Start() error {
	_, err := p.ListenOn("127.0.0.1")
	return err
}

// Addr is the loopback listener's host:port, "" before Start.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addrs["127.0.0.1"]
}

// Port is the loopback listener's port, 0 before Start. Every listener
// shares it when it can, so one Landlock port rule covers them all.
func (p *Proxy) Port() int {
	_, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscan(port, &n)
	return n
}

// ListenOn opens (once) a listener on ip — the loopback one, or the
// gateway address of the docker network a sandboxed container sits on —
// and returns its host:port.
func (p *Proxy) ListenOn(ip string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("egress proxy closed")
	}
	if a, ok := p.addrs[ip]; ok {
		return a, nil
	}
	if p.listeners == nil {
		p.listeners, p.addrs, p.allowed = map[string]net.Listener{}, map[string]string{}, map[string]bool{}
		p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	}
	port := "0"
	if a, ok := p.addrs["127.0.0.1"]; ok {
		_, port, _ = net.SplitHostPort(a)
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip, port))
	if err != nil && port != "0" {
		l, err = net.Listen("tcp", net.JoinHostPort(ip, "0"))
	}
	if err != nil {
		return "", fmt.Errorf("egress proxy: listen on %s: %w", ip, err)
	}
	p.listeners[ip] = l
	p.addrs[ip] = l.Addr().String()
	go func() { _ = p.srv.Serve(l) }()
	return p.addrs[ip], nil
}

// UnixSocket opens (once) a listener on a unix socket in a private
// directory and returns its path. A command in its own network namespace
// has no address to reach the proxy by, but a bind-mounted socket file
// crosses the namespace: the sandbox bridges loopback inside to this.
func (p *Proxy) UnixSocket() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("egress proxy closed")
	}
	if p.sockPath != "" {
		return p.sockPath, nil
	}
	if p.srv == nil {
		p.listeners, p.addrs, p.allowed = map[string]net.Listener{}, map[string]string{}, map[string]bool{}
		p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	}
	dir, err := os.MkdirTemp("", "janetcode-egress-")
	if err != nil {
		return "", fmt.Errorf("egress proxy: %w", err)
	}
	path := filepath.Join(dir, "proxy.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("egress proxy: listen on %s: %w", path, err)
	}
	// Directory 0700 keeps other users out; the file itself is the
	// proxy, which a sandboxed command is meant to reach.
	_ = os.Chmod(path, 0o600)
	p.listeners["unix:"+path] = l
	p.sockDir, p.sockPath = dir, path
	go func() { _ = p.srv.Serve(l) }()
	return path, nil
}

// Close stops every listener and the tunnels they carry.
func (p *Proxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.sockDir != "" {
		defer os.RemoveAll(p.sockDir)
	}
	if p.srv != nil {
		return p.srv.Close()
	}
	return nil
}

// decide is the verdict on one host, asking when the policy says to.
func (p *Proxy) decide(host string) bool {
	switch p.Policy.Decide(host) {
	case Allow:
		return true
	case Block:
		return false
	}
	p.mu.Lock()
	ok := p.allowed[host]
	p.mu.Unlock()
	if ok {
		return true
	}
	if p.Ask == nil || !p.Ask(host) {
		return false
	}
	p.mu.Lock()
	p.allowed[host] = true
	p.mu.Unlock()
	return true
}

func (p *Proxy) rec() *Recorder {
	if p.Recorder == nil {
		return nil
	}
	return p.Recorder()
}

// what is how the report describes traffic through the proxy.
const proxyWhat = "sandboxed command (egress proxy)"

func (p *Proxy) refuse(w http.ResponseWriter, host string) {
	p.rec().Add(Entry{Kind: KindCommand, Host: host, What: proxyWhat, Blocked: true})
	http.Error(w, fmt.Sprintf("janetcode egress policy: %s is not on the allow list (egress.allow); the connection was refused", host), http.StatusForbidden)
}

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

// ServeHTTP handles one proxied request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" || (r.URL.Scheme != "http" && r.URL.Scheme != "") {
		http.Error(w, "janetcode egress proxy: send an absolute http:// URL, or CONNECT for https", http.StatusBadRequest)
		return
	}
	host := strings.ToLower(r.URL.Hostname())
	if p.canaryIn(w, r, host) {
		return
	}
	if !p.decide(host) {
		p.refuse(w, host)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	body := &counter{r: r.Body}
	if r.Body != nil {
		out.Body = body
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return p.dial(ctx, addr) }}
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "janetcode egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	p.rec().Add(Entry{Kind: KindCommand, Host: host, What: proxyWhat, Bytes: body.n.Load()})
}

// connect opens a tunnel for CONNECT host:port.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "janetcode egress proxy: CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if p.canaryIn(w, r, host) {
		return
	}
	if !p.decide(host) {
		p.refuse(w, host)
		return
	}
	up, err := p.dial(r.Context(), r.Host)
	if err != nil {
		http.Error(w, "janetcode egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "janetcode egress proxy: cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	var sent atomic.Int64
	done := make(chan struct{}, 2)
	go func() {
		// What the client had already sent after the CONNECT line.
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			m, _ := up.Write(b)
			sent.Add(int64(m))
		}
		n, _ := io.Copy(up, client)
		sent.Add(n)
		if c, ok := up.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, up)
		if c, ok := client.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	client.Close()
	up.Close()
	p.rec().Add(Entry{Kind: KindCommand, Host: host, What: proxyWhat, Bytes: sent.Load()})
}

// canaryPeek is how much of a plain request's body is searched for a
// honeytoken. An exfiltration sends the environment, which fits.
const canaryPeek = 64 << 10

// canaryIn refuses a request that carries a honeytoken and reports it.
// The body it read to look is put back in front of the rest.
func (p *Proxy) canaryIn(w http.ResponseWriter, r *http.Request, host string) bool {
	if p.Canaries == nil {
		return false
	}
	cs := p.Canaries()
	if len(cs) == 0 {
		return false
	}
	var sb strings.Builder
	sb.WriteString(r.Host + "\n" + r.URL.String() + "\n")
	for k, vs := range r.Header {
		sb.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	if r.Method != http.MethodConnect && r.Body != nil {
		head, _ := io.ReadAll(io.LimitReader(r.Body, canaryPeek))
		sb.Write(head)
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	}
	if _, hit := MatchCanary(cs, sb.String()); !hit {
		return false
	}
	p.rec().Add(Entry{Kind: KindCommand, Host: host, What: proxyWhat + ": honeytoken", Blocked: true})
	if p.OnCanary != nil {
		p.OnCanary(host)
	}
	http.Error(w, "janetcode egress proxy: this request carried a planted honeytoken and was refused; the run is stopped", http.StatusForbidden)
	return true
}

type counter struct {
	r io.ReadCloser
	n atomic.Int64
}

func (c *counter) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

func (c *counter) Close() error { return c.r.Close() }
