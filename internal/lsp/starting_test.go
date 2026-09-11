package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The manager's lock used to be held across the whole handshake, which is
// allowed thirty seconds. On the first edit of a cold rust-analyzer or
// clangd file that froze everything else that asks the manager anything:
// /lsp printed nothing until the server was up, and so did every other
// diagnostic.
func TestAStartingServerDoesNotFreezeTheManager(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--slow-init=3"))
	defer m.Close()
	if err := os.WriteFile(filepath.Join(root, "a.foo"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := m.Diagnose(context.Background(), "a.foo")
		done <- err
	}()
	<-started
	// Give the starter time to get as far as the handshake.
	time.Sleep(300 * time.Millisecond)
	asked := make(chan []string, 1)
	go func() { asked <- m.Status() }()
	select {
	case st := <-asked:
		if !strings.Contains(strings.Join(st, "\n"), "fake:") {
			t.Errorf("status:\n%s", strings.Join(st, "\n"))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/lsp waited on a handshake it had nothing to do with")
	}
	if err := <-done; err != nil {
		t.Fatalf("diagnose: %v", err)
	}
}

// A second caller for the same server must wait on the start in flight
// rather than launch one of its own, and must be free to give up on its
// own context without touching the server everyone else is waiting for.
func TestASecondCallerWaitsOnTheStartInFlight(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--slow-init=2"))
	defer m.Close()
	for _, name := range []string{"a.foo", "b.foo"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := make(chan error, 1)
	go func() {
		_, _, err := m.Diagnose(context.Background(), "a.foo")
		first <- err
	}()
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := m.Diagnose(ctx, "b.foo"); err == nil {
		t.Fatal("the impatient caller should have given up on its own context")
	}
	if err := <-first; err != nil {
		t.Fatalf("the patient caller lost its server: %v", err)
	}
	if st := strings.Join(m.Status(), "\n"); !strings.Contains(st, "fake: running") {
		t.Fatalf("status:\n%s", st)
	}
}

// Close during a handshake used to leave the server running: the manager
// emptied its map, and the start that was still in flight put its server
// into it afterwards, where nothing would ever stop it.
func TestCloseCancelsAServerThatIsStillStarting(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--slow-init=30"))
	if err := os.WriteFile(filepath.Join(root, "a.foo"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Diagnose(context.Background(), "a.foo")
	}()
	time.Sleep(300 * time.Millisecond)
	m.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not cancel the handshake")
	}
	m.mu.Lock()
	left := len(m.clients) + len(m.starting)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d server(s) left after Close", left)
	}
}
