//go:build linux

package shell

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// listeningProxy is a fake egress proxy that really listens where the
// sandbox asks it to, and says hello to whoever connects.
type listeningProxy struct {
	t    *testing.T
	port *string
}

func (l listeningProxy) Port() int { return 0 }
func (l listeningProxy) UnixSocket() (string, error) {
	return "", fmt.Errorf("no socket")
}
func (l listeningProxy) ListenOn(ip string) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return "", err
	}
	serveHello(l.t, ln)
	*l.port = fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	return ln.Addr().String(), nil
}

// TestDockerEgressReachesOnlyTheProxy is the hole the old internal
// network left: the host's own address on it, where every service
// listening on all interfaces answers. A listener on 0.0.0.0 stands in
// for such a service. From the container the proxy (through the sidecar)
// is reachable; that service, by the bridge's gateway or any other
// address the container can route to, is not.
func TestDockerEgressReachesOnlyTheProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("starts containers")
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("no docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bin, "info").Run(); err != nil {
		t.Skip("docker daemon unavailable")
	}
	s := &Sandbox{}
	if err := exec.CommandContext(ctx, bin, "image", "inspect", s.Image()).Run(); err != nil {
		t.Skipf("image %s not present locally (tests do not pull)", s.Image())
	}
	hostSvc, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	serveHello(t, hostSvc)
	svcPort := hostSvc.Addr().(*net.TCPAddr).Port

	var proxyPort string
	s.SetEgressProxy(listeningProxy{t, &proxyPort})
	workspace, _ := FindProfile("workspace")
	run := func(script string) string {
		cmd, err := s.docker(ctx, workspace, t.TempDir(), nil, script)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := cmd.CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		// The sidecar leaves on its own once the proxy is gone; the test
		// does not wait for that.
		exec.Command(bin, "rm", "-f", "janetcode-egress-"+proxyPort).Run()
		exec.Command(bin, "network", "rm", EgressNetwork).Run()
	})

	// The proxy variable names the sidecar; a connection to it reaches
	// the proxy and gets its hello.
	out := run(`a=${HTTP_PROXY#http://}; echo "proxy=$a"; nc -w3 ${a%:*} ${a#*:} </dev/null`)
	if !strings.HasSuffix(out, "hi") {
		t.Fatalf("the proxy was unreachable through the sidecar: %q", out)
	}
	// Every gateway-looking address on every route the container has, and
	// the bridge's gateway, must refuse the host service.
	gw, err := exec.CommandContext(ctx, bin, "network", "inspect", "-f", "{{range .IPAM.Config}}{{.Gateway}}{{end}}", "bridge").Output()
	if err != nil {
		t.Fatal(err)
	}
	hostIP := strings.TrimSpace(string(gw))
	script := fmt.Sprintf(`for h in %s 172.31.0.1 172.31.0.254 host.docker.internal; do nc -z -w2 $h %d && echo "REACHED $h"; done; ip route | grep -c default; true`, hostIP, svcPort)
	if out := run(script); strings.Contains(out, "REACHED") {
		t.Fatalf("a host service on 0.0.0.0 was reachable from the container:\n%s", out)
	}
	// And without a policy the container is still on the ordinary bridge.
	s.SetEgressProxy(nil)
	if out := run(fmt.Sprintf(`nc -w3 %s %d </dev/null`, hostIP, svcPort)); !strings.HasSuffix(out, "hi") {
		t.Fatalf("no policy: the bridge lost the host service: %q", out)
	}
}
