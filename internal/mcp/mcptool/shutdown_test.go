//go:build unix

package mcptool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
)

// A server that is still shaking hands when the session ends used to be
// registered into the map Close had already emptied: nothing ever stopped
// it, and an MCP server is nearly always a wrapper around a process that
// holds a port or an index for the rest of the machine's uptime.
func TestAServerThatFinishesStartingAfterCloseIsStopped(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	m := &Manager{Options: mcp.Options{Root: dir, Version: "test", HandshakeTimeout: 20 * time.Second}}

	done := make(chan error, 1)
	started := m.Start(context.Background(), "slow", mcp.ServerConfig{
		Command: os.Args[0],
		Env: map[string]string{
			"MCP_FAKE_SERVER":       "1",
			"MCP_FAKE_SERVER_DELAY": "400ms",
			"MCP_FAKE_SERVER_CHILD": pidfile,
		},
	}, func(_ string, c *mcp.Client, err error) { done <- err })
	if !started {
		t.Fatal("Start refused to begin")
	}

	pid := waitForChildPid(t, pidfile)
	m.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("onDone err = %v, want ErrClosed", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never finished")
	}
	if _, ok := m.Client("slow"); ok {
		t.Fatal("a closed manager handed out a client")
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the server's child (pid %d) outlived Close", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And the manager stays closed.
	if m.Start(context.Background(), "again", mcp.ServerConfig{Command: os.Args[0]}, nil) {
		t.Fatal("a closed manager started another server")
	}
}

func waitForChildPid(t *testing.T, pidfile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(pidfile); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never reported a child pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
