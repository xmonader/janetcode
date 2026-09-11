//go:build unix

package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// rust-analyzer and clangd are the heaviest processes janetcode starts,
// and they are not one process: they fork workers, and some installs are
// a wrapper script around the real binary. Killing only the one we
// started left the rest running for as long as the machine was up.
func TestClosingAServerTakesItsChildrenWithIt(t *testing.T) {
	root := t.TempDir()
	pidfile := filepath.Join(root, "child.pid")
	m := New(root, fakeConfig(t, "--child="+pidfile))
	p := filepath.Join(root, "a.foo")
	if err := os.WriteFile(p, []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Diagnose(context.Background(), "a.foo"); err != nil {
		t.Fatal(err)
	}
	pid := waitForChildPid(t, pidfile)
	if !processAlive(pid) {
		t.Fatalf("the server's child (pid %d) was never running", pid)
	}
	m.Close()
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the server's child (pid %d) outlived Close", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForChildPid(t *testing.T, pidfile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(pidfile)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake server never recorded a child pid in %s", pidfile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processAlive reports whether the pid still names a process. Once the
// child has been reparented it is not ours to wait for, so signal 0 is
// all there is to ask with.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
