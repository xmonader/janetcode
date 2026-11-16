//go:build linux

package shell

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
)

type fakeProxy struct{ port int }

func (f fakeProxy) Port() int                          { return f.port }
func (f fakeProxy) ListenOn(ip string) (string, error) { return fmt.Sprintf("%s:%d", ip, f.port), nil }
func (f fakeProxy) UnixSocket() (string, error)        { return "", fmt.Errorf("no socket") }

// listen accepts and says hello on a loopback port.
func listen(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { ln.Close() }
}

// TestLandlockEgressProxyIsTheOnlyWayOut is the kernel narrowing a
// workspace-profile command's network to the egress proxy: a connect to
// the proxy's port works, a connect to any other port (standing in for
// the internet) does not, UDP and unix sockets are gone, and the proxy
// variables point at the proxy.
func TestLandlockEgressProxyIsTheOnlyWayOut(t *testing.T) {
	if landlockABI() < 4 {
		t.Skip("this kernel's Landlock cannot narrow TCP")
	}
	if !seccompSupported {
		t.Skip("no socket filter for this architecture")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	proxyPort, stop := listen(t)
	defer stop()
	otherPort, stop2 := listen(t)
	defer stop2()
	s := &Sandbox{}
	s.SetEgressProxy(fakeProxy{proxyPort})
	workspace, _ := FindProfile("workspace")
	run := func(script string) (string, error) {
		cmd, err := s.landlock(context.Background(), workspace, t.TempDir(), nil, "python3 -c '"+script+"'")
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	connect := func(port int) string {
		return fmt.Sprintf(`import socket; s=socket.create_connection(("127.0.0.1", %d), 2); print("reached", s.recv(2).decode())`, port)
	}
	if out, err := run(connect(proxyPort)); err != nil || !strings.Contains(out, "reached hi") {
		t.Fatalf("the proxy's port was refused: %v\n%s", err, out)
	}
	if out, err := run(connect(otherPort)); err == nil {
		t.Fatalf("a connect around the proxy worked:\n%s", out)
	}
	if out, err := run(`import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b"x", ("8.8.8.8", 53)); print("reached")`); err == nil {
		t.Fatalf("UDP (a DNS query around the proxy) worked:\n%s", out)
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
	if out, _ := run(`import os; print(os.environ.get("https_proxy"), os.environ.get("HTTP_PROXY"), repr(os.environ.get("NO_PROXY")))`); !strings.Contains(out, want+" "+want+" ''") {
		t.Fatalf("proxy variables not set: %s", out)
	}
	// Without the proxy the workspace profile's network is untouched.
	s.SetEgressProxy(nil)
	if out, err := run(connect(otherPort)); err != nil || !strings.Contains(out, "reached hi") {
		t.Fatalf("workspace lost its network with no egress policy: %v\n%s", err, out)
	}
}

func TestLandlockEgressRequiresSocketFilter(t *testing.T) {
	if landlockSocketFilter {
		t.Skip("this architecture has a socket filter")
	}
	s := stubSandbox(t, BackendLandlock, 4)
	if err := s.SetProfile("workspace"); err != nil {
		t.Fatal(err)
	}
	s.SetEgressProxy(fakeProxy{4321})
	if err := s.Enable(); err == nil {
		t.Fatal("Landlock egress enabled without the socket filter it needs")
	}
}

func TestAutoBackendWithEgressPrefersBwrapBridge(t *testing.T) {
	s := stubSandbox(t, "", 4)
	s.probe = func() bool { return true }
	if err := s.SetProfile("workspace"); err != nil {
		t.Fatal(err)
	}
	s.SetEgressProxy(fakeProxy{4321})
	want := BackendDocker
	if egressBridgeSupported() {
		want = BackendBwrap
	}
	if backend := s.Backend(); backend != want {
		t.Fatalf("backend with egress = %q, want %q", backend, want)
	}
}

func TestWithProxyEnvReplacesAndClearsNoProxy(t *testing.T) {
	env := WithProxyEnv([]string{"PATH=/bin", "https_proxy=http://corp:3128", "NO_PROXY=internal"}, "http://127.0.0.1:9")
	got := strings.Join(env, " ")
	if strings.Contains(got, "corp") || strings.Contains(got, "internal") || !strings.Contains(got, "PATH=/bin") || !strings.Contains(got, "https_proxy=http://127.0.0.1:9") || !strings.Contains(got, "NO_PROXY=") {
		t.Fatal(got)
	}
}

func TestSeatbeltEgressProfileAllowsOnlyTheProxy(t *testing.T) {
	workspace, _ := FindProfile("workspace")
	pol := seatbeltPolicy(workspace, "/w", nil, 4321)
	if !strings.Contains(pol, "(deny network*)") || !strings.Contains(pol, `(allow network-outbound (remote tcp "localhost:4321"))`) || strings.Contains(pol, "(allow network*)") {
		t.Fatal(pol)
	}
	if pol := SeatbeltPolicy(workspace, "/w", nil); !strings.Contains(pol, "(allow network*)") {
		t.Fatal("no proxy should leave the network open:\n" + pol)
	}
}
