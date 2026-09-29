package mcptool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/tool"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func statusOf(m *Manager, name string) (ServerStatus, bool) {
	for _, st := range m.Status() {
		if st.Name == name {
			return st, true
		}
	}
	return ServerStatus{}, false
}

// A server that dies mid-session used to stay listed as up with tools
// that answered "connection lost" forever. It is now marked down, its
// tools say so without calling anything, and it is started again.
func TestAServerThatExitsIsMarkedDownAndReconnected(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "died")
	reg := tool.NewRegistry()
	var mu sync.Mutex
	var notes []string
	// The first reconnect waits long enough for the test to see the
	// server down and call its tool; later ones are immediate.
	gate := make(chan struct{})
	m := &Manager{
		Options:  mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second},
		Registry: reg,
		Notify: func(s string) {
			mu.Lock()
			notes = append(notes, s)
			mu.Unlock()
		},
		Backoff: func(int) time.Duration { <-gate; return 10 * time.Millisecond },
	}
	defer m.Close()
	cfg := mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1", "MCP_FAKE_EXIT_ONCE": marker}}
	done := make(chan error, 1)
	m.Start(context.Background(), "flaky", cfg, func(_ string, _ *mcp.Client, err error) {
		select {
		case done <- err:
		default:
		}
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	first, _ := m.Client("flaky")

	// Backoff blocks inside the watcher until the gate opens, so the
	// down state is observable: close the gate after checking.
	waitFor(t, "the exit to be noticed", func() bool {
		select {
		case <-first.Done():
			return true
		default:
			return false
		}
	})
	echo, ok := reg.Get("mcp__flaky__echo")
	if !ok {
		t.Fatal("the down server's tools were taken away; a call would read as an unknown tool")
	}
	_, err := echo.Run(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "is down") || !strings.Contains(err.Error(), "/mcp reconnect flaky") {
		t.Fatalf("call to a down server = %v, want the down message", err)
	}
	close(gate)

	waitFor(t, "the reconnect", func() bool {
		c, ok := m.Client("flaky")
		return ok && c != first
	})
	echo, _ = reg.Get("mcp__flaky__echo")
	out, err := echo.Run(context.Background(), json.RawMessage(`{"text":"back"}`))
	if err != nil || !strings.Contains(out, "echo: back") {
		t.Fatalf("after reconnect: %q, %v", out, err)
	}
	st, _ := statusOf(m, "flaky")
	if st.Down || st.Err != nil {
		t.Fatalf("status after reconnect = %+v", st)
	}
	mu.Lock()
	joined := strings.Join(notes, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "mcp flaky disconnected") || !strings.Contains(joined, "mcp flaky reconnected") {
		t.Fatalf("notes = %q", joined)
	}
}

// After the attempts are spent the server stays down, and /mcp
// reconnect brings it back on demand.
func TestReconnectGivesUpThenComesBackOnDemand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "died")
	m := &Manager{
		Options:     mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second},
		Registry:    tool.NewRegistry(),
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return time.Millisecond },
	}
	defer m.Close()
	good := mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1", "MCP_FAKE_EXIT_ONCE": marker}}
	done := make(chan error, 1)
	m.Start(context.Background(), "s", good, func(_ string, _ *mcp.Client, err error) {
		select {
		case done <- err:
		default:
		}
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Make every restart fail by pointing the recorded launch at nothing.
	m.mu.Lock()
	spec := m.launch["s"]
	spec.cfg = mcp.ServerConfig{Command: filepath.Join(t.TempDir(), "missing")}
	m.launch["s"] = spec
	m.mu.Unlock()

	waitFor(t, "giving up", func() bool {
		st, _ := statusOf(m, "s")
		return st.GaveUp
	})
	st, _ := statusOf(m, "s")
	if !st.Down || st.Attempts != 2 || len(st.Tools) == 0 {
		t.Fatalf("status = %+v, want down after 2 attempts with its tools listed", st)
	}

	m.mu.Lock()
	spec.cfg = good
	m.launch["s"] = spec
	m.mu.Unlock()
	back := make(chan error, 1)
	if !m.Reconnect("s", func(_ string, _ *mcp.Client, err error) { back <- err }) {
		t.Fatal("Reconnect refused a configured server")
	}
	if err := <-back; err != nil {
		t.Fatal(err)
	}
	st, _ = statusOf(m, "s")
	if st.Down || st.GaveUp {
		t.Fatalf("after /mcp reconnect: %+v", st)
	}
	if m.Reconnect("nobody", nil) {
		t.Fatal("Reconnect started a server it was never given")
	}
}

// A server stopped on purpose is not brought back.
func TestStoppedServerIsNotReconnected(t *testing.T) {
	m := &Manager{Options: mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second}, Registry: tool.NewRegistry(), Backoff: func(int) time.Duration { return time.Millisecond }}
	defer m.Close()
	done := make(chan error, 1)
	m.Start(context.Background(), "s", mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}, func(_ string, _ *mcp.Client, err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m.Stop("s")
	time.Sleep(300 * time.Millisecond)
	if _, ok := m.Client("s"); ok {
		t.Fatal("a stopped server came back")
	}
	if st, ok := statusOf(m, "s"); ok {
		t.Fatalf("a stopped server is listed: %+v", st)
	}
}

// notifications/tools/list_changed reloads the tools and registers the
// new one.
func TestToolsListChangedRegistersNewTools(t *testing.T) {
	reg := tool.NewRegistry()
	m := &Manager{Options: mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second}, Registry: reg}
	defer m.Close()
	done := make(chan error, 1)
	m.Start(context.Background(), "g", mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}, func(_ string, _ *mcp.Client, err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("mcp__g__extra"); ok {
		t.Fatal("extra registered before the server offered it")
	}
	echo, _ := reg.Get("mcp__g__echo")
	if _, err := echo.Run(context.Background(), json.RawMessage(`{"text":"grow"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new tool", func() bool {
		_, ok := reg.Get("mcp__g__extra")
		return ok
	})
}

// A start's onDone answers that start only. The reconnect after a crash
// called it again: StartAll's onDone counts itself off a WaitGroup, so
// the first reconnect of a server started that way (every headless run's)
// panicked with a negative WaitGroup counter, live on 2026-09-30.
func TestReconnectDoesNotCallTheStartsOnDoneAgain(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "died")
	m := &Manager{
		Options:  mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second},
		Registry: tool.NewRegistry(),
		Backoff:  func(int) time.Duration { return 10 * time.Millisecond },
	}
	defer m.Close()
	var mu sync.Mutex
	calls := 0
	cfg := mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1", "MCP_FAKE_EXIT_ONCE": marker}}
	m.StartAll(context.Background(), map[string]mcp.ServerConfig{"flaky": cfg}, func(string, *mcp.Client, error) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	first, ok := m.Client("flaky")
	if !ok {
		t.Fatal("the server did not start")
	}
	waitFor(t, "the reconnect", func() bool {
		c, ok := m.Client("flaky")
		return ok && c != first
	})
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("StartAll's onDone ran %d times, want once", calls)
	}
}
