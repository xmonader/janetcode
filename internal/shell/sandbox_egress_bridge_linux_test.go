//go:build linux

package shell

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unixProxy is a fake egress proxy: a loopback port number to point the
// command at, and a unix socket that says hello.
type unixProxy struct {
	port int
	sock string
}

func (u unixProxy) Port() int                          { return u.port }
func (u unixProxy) ListenOn(ip string) (string, error) { return fmt.Sprintf("%s:%d", ip, u.port), nil }
func (u unixProxy) UnixSocket() (string, error)        { return u.sock, nil }

func serveHello(t *testing.T, ln net.Listener) {
	t.Helper()
	t.Cleanup(func() { ln.Close() })
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
}

// TestBwrapEgressBridgeIsTheOnlyWayOut is the hole Landlock leaves: a
// remote host listening on the proxy's port. 127.0.0.2 on the same port
// stands in for it. Inside the command's own network namespace the proxy
// port reaches the bridge and nothing else: not that host, not UDP, not
// a path-named unix socket, and the exit status survives the bridge.
func TestBwrapEgressBridgeIsTheOnlyWayOut(t *testing.T) {
	if !egressBridgeSupported() {
		t.Skip("no socket filter for this architecture")
	}
	s := &Sandbox{}
	if !s.bwrapUsable() {
		t.Skip("bubblewrap cannot run here")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	sock := filepath.Join(t.TempDir(), "proxy.sock")
	uln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	serveHello(t, uln)
	// The port number the proxy "has", and a host on another address
	// listening on that same port.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	remote, err := net.Listen("tcp", fmt.Sprintf("127.0.0.2:%d", port))
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	serveHello(t, remote)

	s.SetEgressProxy(unixProxy{port, sock})
	workspace, _ := FindProfile("workspace")
	run := func(script string) (string, error) {
		cmd, err := s.bwrap(context.Background(), workspace, t.TempDir(), nil, "python3 -c '"+script+"'")
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	connect := func(host string) string {
		return fmt.Sprintf(`import socket; s=socket.create_connection(("%s", %d), 2); print("reached", s.recv(2).decode())`, host, port)
	}
	if out, err := run(connect("127.0.0.1")); err != nil || !strings.Contains(out, "reached hi") {
		t.Fatalf("the proxy through the bridge was unreachable: %v\n%s", err, out)
	}
	if out, err := run(connect("127.0.0.2")); err == nil {
		t.Fatalf("a remote host on the proxy's port was reachable:\n%s", out)
	}
	if out, err := run(`import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b"x", ("8.8.8.8", 53)); print("reached")`); err == nil {
		t.Fatalf("UDP worked:\n%s", out)
	}
	if out, err := run(fmt.Sprintf(`import socket; s=socket.socket(socket.AF_UNIX); s.connect("%s"); print("reached", s.recv(2).decode())`, sock)); err == nil {
		t.Fatalf("a path-named unix socket was reachable:\n%s", out)
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	if out, _ := run(`import os; print(os.environ.get("https_proxy"))`); !strings.Contains(out, want) {
		t.Fatalf("proxy variable not set: %s", out)
	}
	// Exit status passes through the bridge and the filter stage.
	cmd, err := s.bwrap(context.Background(), workspace, t.TempDir(), nil, "exit 7")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 7 {
		t.Fatalf("exit status lost: %v", err)
	}
	// Without a policy the network is as before.
	s.SetEgressProxy(nil)
	if out, err := run(connect("127.0.0.2")); err != nil || !strings.Contains(out, "reached hi") {
		t.Fatalf("workspace lost its network with no egress policy: %v\n%s", err, out)
	}
}
