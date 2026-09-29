package weburl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// GuardDial returns rt with a dialer that refuses to connect to a private
// address. PrivateTarget reads only what the URL spells, and a name can
// resolve anywhere: "localhost." with its root dot, any subdomain of
// localhost, a wildcard-DNS name like 127.0.0.1.nip.io, a server that
// answers with a public address when asked early and 169.254.169.254 when
// asked again. Judging the address the socket is about to connect to,
// after resolution, covers every one of them and every redirect as well,
// and leaves DNS no later moment to change its answer in.
//
// A proxy is exempt: it is the user's own configuration, may well sit on a
// private address, and resolves the target itself, so for a proxied
// request only the literal check applies. Which proxy that is, is asked
// of the transport's Proxy for every request it sends — HTTP_PROXY and
// HTTPS_PROXY can differ, NO_PROXY exempts some hosts, and a redirect
// from http to https changes which applies — so the addresses exempted
// are the ones Proxy actually handed out, not the one it named for the
// first request. A RoundTripper that is not an *http.Transport has no
// dialer to guard and is returned as is.
//
// keepAlives says whether idle connections may be kept for reuse: a
// one-shot fetch owns its transport and must not leave one behind, while
// a long-lived client (a remote MCP server) reuses them.
func GuardDial(rt http.RoundTripper, keepAlives bool) http.RoundTripper {
	var tr *http.Transport
	switch v := rt.(type) {
	case nil:
		tr = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		tr = v.Clone()
	default:
		return rt
	}
	var mu sync.Mutex
	proxies := map[string]bool{}
	if pick := tr.Proxy; pick != nil {
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			p, err := pick(r)
			if err == nil && p != nil {
				port := p.Port()
				if port == "" {
					port = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[p.Scheme]
				}
				mu.Lock()
				proxies[strings.ToLower(net.JoinHostPort(p.Hostname(), port))] = true
				mu.Unlock()
			}
			return p, err
		}
	}
	direct := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	checked := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: RefusePrivateDial}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		proxy := proxies[strings.ToLower(addr)]
		mu.Unlock()
		if proxy {
			return direct.DialContext(ctx, network, addr)
		}
		return checked.DialContext(ctx, network, addr)
	}
	tr.DisableKeepAlives = !keepAlives
	return tr
}

// RefusePrivateDial is a net.Dialer Control: address is the resolved
// ip:port about to be connected to.
func RefusePrivateDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refused: cannot tell where %q is", address)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("refused: cannot tell where %q is", address)
	}
	if why := PrivateIP(net.IP(a.WithZone("").AsSlice())); why != "" {
		return fmt.Errorf("refused: the name resolves to %s, which is %s, and you were not asked to fetch it. Only a URL the user named themselves may point inside the local machine or network", a.WithZone(""), why)
	}
	return nil
}
