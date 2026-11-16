package shell

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// EgressProxy is what the sandbox needs from the egress policy's
// filtering proxy (internal/egress.Proxy): the loopback port to narrow
// the network to, and a listener on a docker network's gateway address.
type EgressProxy interface {
	Port() int
	ListenOn(ip string) (string, error)
	UnixSocket() (string, error)
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
		if !egressBridgeSupported() {
			return "this build cannot bridge bubblewrap's network namespace to the egress proxy, so sandboxed commands are refused while the policy is active"
		}
		return "bash runs in a network namespace of its own whose only way out is a bridge to the egress proxy; UDP and unix sockets are taken away too"
	case BackendLandlock:
		if s.landlockABI() < 4 {
			return fmt.Sprintf("Landlock ABI %d cannot narrow TCP to the egress proxy (needs ABI 4, Linux 6.7), so sandboxed commands are refused while the policy is active", s.landlockABI())
		}
		if !landlockSocketFilter {
			return "this architecture has no socket filter to take UDP and unix sockets away, so sandboxed commands are refused while the policy is active"
		}
		return "bash can connect only to the egress proxy (Landlock allows TCP connect to its port alone; a seccomp filter takes UDP and unix sockets away). The rule is by port: a remote host listening on that same port is reachable too"
	case BackendDocker:
		return "bash's container sits on the internal, host-less docker network " + EgressNetwork + ", where the only thing it can reach is a sidecar that forwards to the egress proxy (the sidecar image needs busybox nc, as the default alpine has)"
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

// EgressNetwork is the docker network a sandboxed container is put on
// while an egress policy is active. It is internal (no route out) and
// "isolated" (the host has no address on it), so the container cannot
// reach the host's services, which an ordinary internal network's gateway
// address still lets it do. The one thing on it is the egress sidecar.
const EgressNetwork = "janetcode-egress-isolated"

// egressSidecarPort is the port the sidecar listens on, on EgressNetwork.
const egressSidecarPort = 3128

var sidecarMu sync.Mutex

func dockerOut(ctx context.Context, bin string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// dockerEgressSidecar makes sure the isolated network and the sidecar
// exist and returns the host:port a container on the network reaches the
// egress proxy at.
//
// The sidecar is a container on both the default bridge and the isolated
// network. It forwards its port to the proxy, listening on the bridge's
// gateway address, with busybox nc, which the default image has, and it
// exits (and is removed) once the proxy stops answering, so nothing
// outlives the session. The command's container sees the sidecar and
// nothing else.
func (s *Sandbox) dockerEgressSidecar(ctx context.Context, bin string, px EgressProxy) (string, error) {
	sidecarMu.Lock()
	defer sidecarMu.Unlock()
	if _, err := dockerOut(ctx, bin, "network", "inspect", EgressNetwork); err != nil {
		if out, err := dockerOut(ctx, bin, "network", "create", "--internal",
			"-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated", EgressNetwork); err != nil && !strings.Contains(out, "already exists") {
			return "", fmt.Errorf("docker network create %s: %s (needs Docker 28 or newer)", EgressNetwork, out)
		}
	}
	hostGW, err := dockerOut(ctx, bin, "network", "inspect", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}", "bridge")
	if err != nil || len(strings.Fields(hostGW)) == 0 {
		return "", fmt.Errorf("egress policy: docker's bridge network has no gateway: %s", hostGW)
	}
	hostGW = strings.Fields(hostGW)[0]
	proxyAddr, err := px.ListenOn(hostGW)
	if err != nil {
		return "", err
	}
	_, proxyPort, _ := net.SplitHostPort(proxyAddr)
	name := "janetcode-egress-" + proxyPort
	ip := func() string {
		out, err := dockerOut(ctx, bin, "inspect", "-f",
			`{{if .State.Running}}{{(index .NetworkSettings.Networks "`+EgressNetwork+`").IPAddress}}{{end}}`, name)
		if err != nil {
			return ""
		}
		return out
	}
	if a := ip(); a != "" {
		return net.JoinHostPort(a, fmt.Sprint(egressSidecarPort)), nil
	}
	_, _ = dockerOut(ctx, bin, "rm", "-f", name) // a stopped leftover
	script := fmt.Sprintf(`nc -lk -p %d -e nc %s %s & S=$!; while nc -z -w2 %s %s; do sleep 5; done; kill $S`,
		egressSidecarPort, hostGW, proxyPort, hostGW, proxyPort)
	if out, err := dockerOut(ctx, bin, "run", "-d", "--rm", "--name", name, "--network", "bridge", s.Image(), "sh", "-c", script); err != nil {
		return "", fmt.Errorf("egress sidecar: %s", out)
	}
	if out, err := dockerOut(ctx, bin, "network", "connect", EgressNetwork, name); err != nil {
		_, _ = dockerOut(ctx, bin, "rm", "-f", name)
		return "", fmt.Errorf("egress sidecar: %s", out)
	}
	for i := 0; i < 30; i++ {
		if _, err := dockerOut(ctx, bin, "exec", name, "nc", "-z", "-w1", "127.0.0.1", fmt.Sprint(egressSidecarPort)); err == nil {
			if a := ip(); a != "" {
				return net.JoinHostPort(a, fmt.Sprint(egressSidecarPort)), nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	logs, _ := dockerOut(ctx, bin, "logs", name)
	_, _ = dockerOut(ctx, bin, "rm", "-f", name)
	return "", fmt.Errorf("egress sidecar did not come up (the image %s needs busybox nc): %s", s.Image(), logs)
}
