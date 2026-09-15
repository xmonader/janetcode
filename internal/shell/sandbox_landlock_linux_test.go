//go:build linux

package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLandlockConfinesWrites is the only test that proves the kernel is
// doing the work: a command under a profile whose one write root is the
// project root can write inside it and cannot write next to it. A kernel
// without Landlock skips — detecting is the job, not failing.
func TestLandlockConfinesWrites(t *testing.T) {
	abi := landlockABI()
	if abi < 1 {
		t.Skip("this kernel has no Landlock")
	}
	root := t.TempDir()
	outside := t.TempDir()
	s := &Sandbox{}
	if err := s.SetBackend(BackendLandlock); err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "test", WriteRoots: []string{WriteRootToken}, Network: true}

	inside := filepath.Join(root, "in.txt")
	cmd, err := s.landlock(context.Background(), p, root, nil, "echo ok > "+inside)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("a write inside the root failed: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(inside); err != nil || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("inside = %q, %v", b, err)
	}

	blocked := filepath.Join(outside, "out.txt")
	cmd, err = s.landlock(context.Background(), p, root, nil, "echo bad > "+blocked)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a write outside the write roots succeeded:\n%s", out)
	}
	if _, err := os.Stat(blocked); err == nil {
		t.Fatalf("%s was created despite Landlock", blocked)
	}
}

// TestLandlockReadsStayBroad: the profiles all read broadly, and the
// ruleset therefore handles no read access at all.
func TestLandlockReadsStayBroad(t *testing.T) {
	if landlockABI() < 1 {
		t.Skip("this kernel has no Landlock")
	}
	s := &Sandbox{}
	root := t.TempDir()
	p := Profile{Name: "test", WriteRoots: []string{WriteRootToken}, Network: true}
	cmd, err := s.landlock(context.Background(), p, root, nil, "cat /etc/hostname > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("a read outside the root failed: %v\n%s", err, out)
	}
}

// TestLandlockCommandIsAReExec pins the shape of the command: this
// binary, the sentinel, the shell, and the policy in the environment.
func TestLandlockCommandIsAReExec(t *testing.T) {
	if landlockABI() < 1 {
		t.Skip("this kernel has no Landlock")
	}
	root := t.TempDir()
	s := stubSandbox(t, BackendLandlock, landlockABI())
	if _, err := SetSandbox(s, "workspace"); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.CommandIn(context.Background(), root, nil, "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if cmd.Path != self {
		t.Errorf("path = %q, want this binary %q", cmd.Path, self)
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != landlockSentinel {
		t.Errorf("args = %v, want the landlock sentinel second", cmd.Args)
	}
	if got := strings.Join(cmd.Args, " "); !strings.HasSuffix(got, "echo hi") {
		t.Errorf("args = %q, want the command last", got)
	}
	var policy string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, landlockEnv+"=") {
			policy = strings.TrimPrefix(kv, landlockEnv+"=")
		}
	}
	if !strings.Contains(policy, root) || !strings.Contains(policy, `"net":true`) {
		t.Errorf("policy = %q, want the write root and the network flag", policy)
	}
}
