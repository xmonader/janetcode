package webui

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Links in notifications. A notification hook gets a URL that opens this
// server on the waiting prompt, so a push to a phone is one tap from the
// approval. The URL is a message to a third party (an ntfy topic, a chat
// webhook), so it must not carry the long-lived access token: it carries a
// ticket instead, good for one login within ticketTTL. Whoever opens it
// first gets the session cookie, exactly as if they had opened the printed
// link; a link that was opened, or that sat unread past its time, is a
// dead URL. The cost of one-time: a previewer that fetches the URL before
// you do (some chat apps unfurl links) spends the ticket, and the next
// notification brings a fresh one.

const (
	// publicURLEnv is the address clients reach this server at when it is
	// not the one bound: a reverse proxy, or tailscale serve's https name.
	publicURLEnv = "JANETCODE_WEB_URL"
	// ticketTTL bounds how long a notification's link can sit unread.
	ticketTTL = 30 * time.Minute
	// maxTickets bounds the table: a hook that fires in a loop must not
	// grow it without limit. The oldest ticket is dropped first.
	maxTickets = 64
)

type tickets struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]time.Time // ticket -> expiry
	ord []string             // oldest first
}

func newTickets() *tickets {
	return &tickets{now: time.Now, m: map[string]time.Time{}}
}

// mint returns a fresh ticket.
func (t *tickets) mint() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune()
	for len(t.ord) >= maxTickets {
		delete(t.m, t.ord[0])
		t.ord = t.ord[1:]
	}
	t.m[id] = t.now().Add(ticketTTL)
	t.ord = append(t.ord, id)
	return id, nil
}

// redeem spends a ticket: true once, for an unexpired one.
func (t *tickets) redeem(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	exp, ok := t.m[id]
	if !ok {
		return false
	}
	delete(t.m, id)
	for i, o := range t.ord {
		if o == id {
			t.ord = append(t.ord[:i], t.ord[i+1:]...)
			break
		}
	}
	return t.now().Before(exp)
}

func (t *tickets) prune() {
	now := t.now()
	keep := t.ord[:0]
	for _, id := range t.ord {
		if now.Before(t.m[id]) {
			keep = append(keep, id)
		} else {
			delete(t.m, id)
		}
	}
	t.ord = keep
}

// baseURL is the address a phone or another machine reaches the server
// at: JANETCODE_WEB_URL when set, else the bound address, with a
// wildcard bind replaced by the machine's first private IPv4 address
// (a wildcard is not a place anyone can connect to).
func (s *server) baseURL() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv(publicURLEnv)), "/"); u != "" {
		return u
	}
	host, port, err := net.SplitHostPort(s.opts.Addr)
	if err != nil {
		return "http://" + s.opts.Addr
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		if ip == nil && host != "" {
			return "http://" + net.JoinHostPort(host, port)
		}
		host = privateIPv4()
	}
	return "http://" + net.JoinHostPort(host, port)
}

// privateIPv4 returns this machine's first non-loopback private IPv4
// address, or 127.0.0.1 when it has none.
func privateIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}

// notifyLink makes the URL a notification hook is given: a one-time
// login ticket, and approve=1 when it should open on the pending
// approval. "" when a ticket could not be made.
func (s *server) notifyLink(approval bool) string {
	id, err := s.tickets.mint()
	if err != nil {
		return ""
	}
	q := url.Values{"ticket": {id}}
	if approval {
		q.Set("approve", "1")
	}
	return s.baseURL() + "/?" + q.Encode()
}

// publicHost is the host (with port) of JANETCODE_WEB_URL, "" when it is
// not set. A server bound to loopback answers to loopback Host names
// only; behind tailscale serve or a proxy the Host is this name, and
// only this one is let in besides.
func publicHost() string {
	raw := strings.TrimSpace(os.Getenv(publicURLEnv))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// hostAllowed reports whether the guard may accept a request's Host on a
// loopback-bound server.
func hostAllowed(hostport string) bool {
	if loopbackHost(hostport) {
		return true
	}
	p := publicHost()
	return p != "" && strings.EqualFold(p, hostport)
}
