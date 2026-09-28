// Package weburl holds the web-access rules that do not need the tool
// machinery: URLs the agent is asked to fetch are normalized (GitHub blob
// links rewritten to raw content) and vetted (private-network targets
// recognised, the hosts the user named remembered), and the search
// backend's configuration is validated.
package weburl

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Normalize rewrites URLs that would fetch something other than what
// they appear to. A GitHub blob page is 300 KB of application shell around
// the file; raw.githubusercontent.com is the file. The rewrite happens
// before the permission check so that the host the user approves, and any
// rule they save, is the host actually contacted.
func Normalize(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return raw, false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 5 {
		return raw, false
	}
	if parts[2] != "blob" && parts[2] != "raw" {
		return raw, false
	}
	rest := strings.Join(append(parts[:2], parts[3:]...), "/")
	out := &url.URL{Scheme: "https", Host: "raw.githubusercontent.com", Path: "/" + rest}
	return out.String(), true
}

// Set remembers the hosts the user named in their own messages. A
// fetch of a private-network address is refused unless the user asked for
// that host: an agent reaching 169.254.169.254 or a service on localhost
// off its own bat is reading something the user never offered it, and on
// a cloud instance that is the credentials endpoint.
type Set struct {
	mu    sync.Mutex
	hosts map[string]bool
}

var urlInText = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'` + "`" + `)\]]+`)

// Note records every host named in text.
func (s *Set) Note(text string) {
	if s == nil || text == "" {
		return
	}
	for _, m := range urlInText.FindAllString(text, -1) {
		// Punctuation that ends the sentence or the list item the URL sits
		// in is not part of it: "on http://10.0.0.7:8080." names port 8080,
		// and keeping the dot remembers a host no fetch will ever match.
		m = strings.TrimRight(m, ".,;:!?")
		u, err := url.Parse(m)
		if err != nil || u.Host == "" {
			continue
		}
		s.mu.Lock()
		if s.hosts == nil {
			s.hosts = map[string]bool{}
		}
		s.hosts[strings.ToLower(u.Host)] = true
		s.mu.Unlock()
	}
}

// Has reports whether the user named this host (with or without its port).
func (s *Set) Has(host string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	host = strings.ToLower(host)
	if s.hosts[host] {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return s.hosts[h]
	}
	for known := range s.hosts {
		if h, _, err := net.SplitHostPort(known); err == nil && h == host {
			return true
		}
	}
	return false
}

// cgnat is 100.64.0.0/10, the shared address space (RFC 6598).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// privateName lists hostnames that always mean this machine or this
// network, whatever they resolve to.
var privateName = map[string]bool{
	"localhost": true, "localhost.localdomain": true,
	"metadata.google.internal": true,
}

// PrivateTarget reports why a URL points inside the local machine or
// network, or "" when it does not. Only literal addresses and names that
// can mean nothing else are judged: a name that resolves to a private
// address is caught where the connection is made (PrivateIP, applied by
// the fetch tool's dialer), since judging it here would lose the race
// with DNS.
func PrivateTarget(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if privateName[host] || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return host + " is this machine or this network"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if why := PrivateIP(ip); why != "" {
		return host + " is " + why
	}
	return ""
}

// PrivateIP reports what kind of non-public address ip is ("the loopback
// address", ...), or "" for a public one.
func PrivateIP(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "the loopback address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "a link-local address (on a cloud instance this is the credentials endpoint)"
	case ip.IsPrivate():
		return "a private network address"
	case cgnat.Contains(ip):
		// 100.64/10 is not "private" by net's definition, but nothing on
		// it is the public internet: it is the carrier's own network, and
		// on several clouds it is where internal services live.
		return "a carrier-internal address"
	case ip.IsUnspecified():
		return "the unspecified address"
	}
	return ""
}
