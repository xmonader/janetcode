package shell

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// EgressProxy is what the sandbox needs from the egress policy's
// filtering proxy (internal/egress.Proxy): the loopback port to narrow
// the network to, and a listener on a docker network's gateway address.
type EgressProxy interface {
	Port() int
	ListenOn(ip string) (string, error)
}

// SetEgressProxy routes a sandboxed command's network through p: under a
// profile that has a network, the command gets the proxy variables and
// the backend takes every other way out away. A no-network profile is
// unchanged — it already has none. Nil turns it off.
func (s *Sandbox) SetEgressProxy(p EgressProxy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.proxy = p
	s.mu.Unlock()
}

func (s *Sandbox) egressProxy() EgressProxy {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxy
}

// proxyPort is the loopback port a command under profile p is narrowed
// to, 0 when none applies.
func (s *Sandbox) proxyPort(p Profile) int {
	px := s.egressProxy()
	if px == nil || !p.Network {
		return 0
	}
	return px.Port()
}

// EgressNote says how far the active backend enforces the egress policy
// on bash, for /egress and the doctor. "" when no proxy is set.
func (s *Sandbox) EgressNote() string {
	if s.egressProxy() == nil {
		return ""
	}
	if !s.Enabled() {
		return "bash is not sandboxed, so the egress policy reaches it only through the hosts a command spells out; /sandbox workspace routes every connection through the egress proxy"
	}
	p := s.Profile()
	if !p.Network {
		return "bash runs under the " + p.Name + " profile, which has no network at all"
	}
	backend, err := s.backendFor()
	if err != nil {
		return "the sandbox cannot run here (" + err.Error() + "), so bash is not held to the egress policy"
	}
	switch backend {
	case BackendBwrap:
		return "bubblewrap cannot narrow the network to the egress proxy, so sandboxed commands are refused while the policy is active"
	case BackendLandlock:
		if s.landlockABI() < 4 {
			return fmt.Sprintf("Landlock ABI %d cannot narrow TCP to the egress proxy (needs ABI 4, Linux 6.7), so sandboxed commands are refused while the policy is active", s.landlockABI())
		}
		if !landlockSocketFilter {
			return "this architecture has no socket filter to take UDP and unix sockets away, so sandboxed commands are refused while the policy is active"
		}
		return "bash can connect only to the egress proxy (Landlock allows TCP connect to its port alone; a seccomp filter takes UDP and unix sockets away). The rule is by port: a remote host listening on that same port is reachable too"
	case BackendDocker:
		return "bash's container sits on the internal docker network " + EgressNetwork + ", whose only way out is the egress proxy on its gateway; services on the host that listen on every address are reachable from it too"
	case BackendSeatbelt:
		return "seatbelt allows network-outbound only to the egress proxy on localhost"
	}
	return ""
}

// proxyVars are the variables that point a command's HTTP clients at the
// proxy, both spellings, since curl reads only the lower-case http_proxy.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

// WithProxyEnv returns env with every proxy variable set to proxyURL and
// NO_PROXY cleared: a host exempted from the proxy would only get a
// refused connection from the sandbox.
func WithProxyEnv(env []string, proxyURL string) []string {
	drop := map[string]bool{"NO_PROXY": true, "no_proxy": true}
	for _, v := range proxyVars {
		drop[v] = true
	}
	out := make([]string, 0, len(env)+len(proxyVars)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	for _, v := range proxyVars {
		out = append(out, v+"="+proxyURL)
	}
	return append(out, "NO_PROXY=", "no_proxy=")
}

// EgressNetwork is the internal docker network a sandboxed container is
// put on while an egress policy is active: internal, so docker gives it
// no route out, while the host's own address on its bridge — where the
// egress proxy listens — stays reachable.
const EgressNetwork = "janetcode-egress"

// dockerEgressGateway makes sure the internal network exists and returns
// its gateway address.
func (s *Sandbox) dockerEgressGateway(ctx context.Context, bin string) (string, error) {
	gw := func() (string, error) {
		out, err := exec.CommandContext(ctx, bin, "network", "inspect", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}", EgressNetwork).Output()
		if err != nil {
			return "", err
		}
		f := strings.Fields(string(out))
		if len(f) == 0 {
			return "", fmt.Errorf("docker network %s has no gateway", EgressNetwork)
		}
		return f[0], nil
	}
	if g, err := gw(); err == nil {
		return g, nil
	}
	if out, err := exec.CommandContext(ctx, bin, "network", "create", "--internal", EgressNetwork).CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		return "", fmt.Errorf("docker network create %s: %s", EgressNetwork, strings.TrimSpace(string(out)))
	}
	return gw()
}
