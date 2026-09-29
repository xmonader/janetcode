package egress

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Verdict is what the policy says about one host.
type Verdict int

const (
	Allow Verdict = iota
	Ask
	Block
)

func (v Verdict) String() string {
	return [...]string{"allow", "ask", "block"}[v]
}

// ParseVerdict reads the config's "default".
func ParseVerdict(s string) (Verdict, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "allow":
		return Allow, nil
	case "ask":
		return Ask, nil
	case "block", "deny":
		return Block, nil
	}
	return Allow, fmt.Errorf("egress.default %q: want allow, ask or block", s)
}

// Policy decides which hosts a turn may send data to. The model
// provider's own hosts are always allowed (Implicit): refusing them would
// refuse the conversation itself.
type Policy struct {
	Default  Verdict
	Allow    []string
	Implicit []string
}

// Active reports whether the policy restricts anything.
func (p *Policy) Active() bool { return p != nil && p.Default != Allow }

// Decide is the verdict for host. Patterns are exact hosts, "*.example.com"
// (any subdomain) or ".example.com" (the domain and any subdomain).
func (p *Policy) Decide(host string) Verdict {
	if p == nil || p.Default == Allow {
		return Allow
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return p.Default
	}
	for _, list := range [][]string{p.Implicit, p.Allow} {
		for _, pat := range list {
			if matchHost(strings.ToLower(strings.TrimSpace(pat)), host) {
				return Allow
			}
		}
	}
	return p.Default
}

func matchHost(pat, host string) bool {
	switch {
	case pat == "":
		return false
	case pat == "*":
		return true
	case strings.HasPrefix(pat, "*."):
		return strings.HasSuffix(host, pat[1:])
	case strings.HasPrefix(pat, "."):
		return host == pat[1:] || strings.HasSuffix(host, pat)
	}
	return host == pat
}

// Validate rejects patterns that cannot match a host.
func (p *Policy) Validate() error {
	for _, pat := range p.Allow {
		if strings.ContainsAny(pat, "/: ") {
			return fmt.Errorf("egress.allow %q: a host or *.domain, not a URL", pat)
		}
	}
	return nil
}

// Describe is one line for /egress and the doctor.
func (p *Policy) Describe() string {
	if !p.Active() {
		return "egress policy: every host allowed (set egress.default to ask or block to restrict it)"
	}
	allow := "none"
	if len(p.Allow) > 0 {
		allow = strings.Join(p.Allow, ", ")
	}
	return fmt.Sprintf("egress policy: %s for hosts not allowed · allowed: %s (plus the model provider)", p.Default, allow)
}

type policyKey struct{}

// WithPolicy returns ctx carrying the policy, which Transport enforces.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// PolicyFrom is the policy ctx carries, or nil.
func PolicyFrom(ctx context.Context) *Policy {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(policyKey{}).(*Policy)
	return p
}

// HostOf is the host of a URL, or "".
func HostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

var (
	urlRe = regexp.MustCompile(`(?i)\b(?:https?|ftp|wss?|git|ssh)://([^\s/'"@]+@)?([a-z0-9.\-]+|\[[0-9a-f:]+\])`)
	// scp-style git remotes and ssh targets: git@github.com:owner/repo
	scpRe = regexp.MustCompile(`(?i)(?:^|[\s'"])[a-z0-9._\-]+@([a-z0-9.\-]+\.[a-z]{2,}|[a-z0-9\-]+):`)
	// A bare host after a command that takes one: ssh host, curl example.com
	bareCmdRe = regexp.MustCompile(`(?i)(?:^|[;&|]\s*|\s)(?:ssh|scp|sftp|rsync|curl|wget|nc|ncat|telnet|ping)\s+(?:-\S+\s+(?:\S+\s+)?)*([a-z0-9][a-z0-9\-]*(?:\.[a-z0-9\-]+)+)(?:\s|$|:|/)`)
)

// HostsInCommand lists the hosts a shell command visibly reaches: URLs,
// user@host: remotes, and the host argument of ssh, curl and the like.
// It is recognition, not proof — a command can reach a host it does not
// spell — which is why a sandbox is what enforces the policy on bash, and
// this only reports and asks.
func HostsInCommand(cmd string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.Trim(strings.ToLower(h), "[]")
		if h == "" || seen[h] || net.ParseIP(h) == nil && !strings.Contains(h, ".") && h != "localhost" {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, m := range urlRe.FindAllStringSubmatch(cmd, -1) {
		add(m[2])
	}
	for _, m := range scpRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1])
	}
	for _, m := range bareCmdRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1])
	}
	return out
}
