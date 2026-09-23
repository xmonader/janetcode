package webui

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// guard keeps other web pages out of the API. The server runs commands,
// so a request it did not get from its own page is refused:
//
//   - Cross-site requests. A page on any site the user has open can POST
//     to 127.0.0.1 — a text/plain body needs no CORS preflight — and
//     "!cmd" to /api/prompt ran a shell command. Browsers say where a
//     request came from (Sec-Fetch-Site, or Origin); anything but the
//     page's own origin is refused. A request with neither header is
//     not from a browser page (curl, a script the user runs) and passes.
//   - DNS rebinding. A hostile name that re-resolves to 127.0.0.1 makes
//     the attacker's page same-origin with the server, so the origin
//     check alone passes it. When the server listens on loopback only,
//     the Host must be a loopback name, which a rebound name is not.
//
// The page itself (/, /static/) stays reachable from a link: opening it
// is not an action, and it can only act through the API, which is
// guarded.
func (s *server) guard(next http.Handler) http.Handler {
	loopback := loopbackAddr(s.opts.Addr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback && !loopbackHost(r.Host) {
			http.Error(w, "forbidden: this server answers only to localhost", http.StatusForbidden)
			return
		}
		if (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events") && crossSite(r) {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossSite reports whether a browser sent r from a page that is not
// this server's own.
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
	default: // same-site (another port on localhost counts), cross-site
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true // "null" from a sandboxed frame or a file:// page
	}
	return !strings.EqualFold(u.Host, r.Host)
}

// loopbackAddr reports whether a listen address binds only loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return isLoopbackName(host)
}

// loopbackHost reports whether a request's Host names this machine.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return isLoopbackName(strings.Trim(host, "[]"))
}

func isLoopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
