//go:build unix

package mcp

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

// A stdio server is nearly always a wrapper — npx, uvx, a shell line —
// around the process that does the work. Closing stdin and killing the
// wrapper leaves that process running, reparented to init, holding its
// port and its memory for the rest of the machine's uptime.
func TestClosingAStdioServerTakesItsChildrenWithIt(t *testing.T) {
	root := t.TempDir()
	pidfile := filepath.Join(root, "child.pid")
	c, err := Start(context.Background(), "fake srv",
		ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1", "MCP_FAKE_SERVER_CHILD": pidfile}},
		Options{Root: root, Version: "test", HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pid := waitForChildPid(t, pidfile)
	if !processAlive(pid) {
		t.Fatalf("the server's child (pid %d) was never running", pid)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the server's child (pid %d) outlived Close", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A server that exits after starting a detached helper which kept its
// stdout and stderr (a daemon started with setsid, out of reach of the
// group signal) must still be noticed as gone. Waiting on the process
// also waits for its output pipes, which the helper holds open, so the
// connection stayed "up" — every call hanging, and Close waiting on it
// forever — for as long as the helper lived.
func TestAServerWhoseDetachedChildHoldsItsPipesIsStillSeenToExit(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "helper.pid")
	c, _ := newStubClient()
	script := "setsid sleep 30 </dev/null & echo $! > " + pidfile + "; exit 0"
	if _, err := newStdioTransport(c, ServerConfig{Command: "sh", Args: []string{"-c", script}}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	pid := waitForChildPid(t, pidfile)
	defer syscall.Kill(pid, syscall.SIGKILL)
	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the server exited, but the client never noticed")
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

// processAlive reports whether the pid still names a process. The child is
// not ours to wait for once it has been reparented, so signal 0 is the
// only thing left to ask with.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
