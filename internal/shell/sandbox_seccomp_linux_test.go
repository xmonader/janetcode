//go:build linux

package shell

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestLandlockNoNetworkTakesUnixAndUDPSocketsToo is the kernel proving
// the no-network profile: before the socket filter, a command under
// strict could still reach a unix socket (docker.sock is the whole
// machine) and send UDP. Now both fail, TCP fails through Landlock, and
// a command that makes no socket runs as before. python3 makes the
// sockets because sh cannot.
func TestLandlockNoNetworkTakesUnixAndUDPSocketsToo(t *testing.T) {
	if landlockABI() < 4 {
		t.Skip("this kernel's Landlock cannot take the network away")
	}
	if !seccompSupported {
		t.Skip("no socket filter for this architecture")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	root := t.TempDir()
	// A listening unix socket outside the sandbox, standing in for the
	// docker daemon.
	sockPath := filepath.Join(t.TempDir(), "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	s := &Sandbox{}
	run := func(p Profile, script string) (string, error) {
		cmd, err := s.landlock(context.Background(), p, root, nil, "python3 -c '"+script+"'")
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	strict, _ := FindProfile("strict")
	for name, script := range map[string]string{
		"unix connect": `import socket; s=socket.socket(socket.AF_UNIX); s.connect("` + sockPath + `"); print("reached")`,
		"udp send":     `import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b"x", ("127.0.0.1", 9)); print("reached")`,
		"tcp connect":  `import socket; s=socket.create_connection(("127.0.0.1", 9), 1); print("reached")`,
		"sctp socket":  `import socket; s=socket.socket(socket.AF_INET, socket.SOCK_STREAM, 132); print("reached")`,
	} {
		if out, err := run(strict, script); err == nil {
			t.Errorf("%s worked under the strict profile:\n%s", name, out)
		}
	}
	if out, err := run(strict, `import socket; a,b=socket.socketpair(); a.send(b"ok"); print(b.recv(2).decode())`); err != nil || !strings.Contains(out, "ok") {
		t.Errorf("socketpair, which reaches nothing outside, was refused: %v\n%s", err, out)
	}
	if out, err := run(strict, `print(open("/etc/hostname").read() != "")`); err != nil || !strings.Contains(out, "True") {
		t.Errorf("an ordinary command failed under the filter: %v\n%s", err, out)
	}
	// The workspace profile has a network, and no filter.
	workspace, _ := FindProfile("workspace")
	if out, err := run(workspace, `import socket; s=socket.socket(socket.AF_UNIX); s.connect("`+sockPath+`"); print("reached")`); err != nil || !strings.Contains(out, "reached") {
		t.Errorf("the workspace profile lost its unix sockets: %v\n%s", err, out)
	}
}

func TestRulesetAttrScopesFromABI6(t *testing.T) {
	if a := rulesetAttr(4, false, false); a.Scoped != 0 || a.Access_net == 0 {
		t.Errorf("ABI 4 no-net: %+v", a)
	}
	if a := rulesetAttr(6, false, false); a.Scoped != unix.LANDLOCK_SCOPE_SIGNAL|unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET {
		t.Errorf("ABI 6 no-net scoped = %#x", a.Scoped)
	}
	if a := rulesetAttr(6, true, false); a.Scoped != unix.LANDLOCK_SCOPE_SIGNAL || a.Access_net != 0 {
		t.Errorf("ABI 6 with network: %+v", a)
	}
}

func TestNoNetFilterAssembles(t *testing.T) {
	if !seccompSupported {
		t.Skip("no socket filter for this architecture")
	}
	ins, err := noNetFilter()
	if err != nil {
		t.Fatal(err)
	}
	last := ins[len(ins)-1]
	if last.Code != unix.BPF_RET|unix.BPF_K {
		t.Fatalf("the program does not end in a return: %+v", last)
	}
}
