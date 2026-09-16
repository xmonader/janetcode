package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// /cd cancels the lifetime any handshake in flight is running on, which is
// how the server for the project being left is stopped. That cancellation
// is not a verdict on the server: recording it as a failure left the
// project just moved into with a language server permanently "unavailable:
// context canceled", and no way back short of restarting janetcode.
func TestRerootDuringAHandshakeDoesNotPoisonTheNewProject(t *testing.T) {
	old := t.TempDir()
	m := New(old, fakeConfig(t, "--slow-init=3"))
	defer m.Close()
	if err := os.WriteFile(filepath.Join(old, "a.foo"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Diagnose(context.Background(), "a.foo")
	}()
	// Let the starter get as far as the handshake, then move house.
	time.Sleep(300 * time.Millisecond)

	fresh := t.TempDir()
	m.Reroot(fresh)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Reroot did not end the handshake it left behind")
	}

	if st := strings.Join(m.Status(), "\n"); strings.Contains(st, "unavailable") {
		t.Fatalf("the new project inherited a failure from the old one:\n%s", st)
	}
	m.mu.Lock()
	left := len(m.clients) + len(m.starting) + len(m.failed)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d stale entries after Reroot", left)
	}

	// And the server still starts in the new tree.
	if err := os.WriteFile(filepath.Join(fresh, "b.foo"), []byte("this has ERR here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, ok, err := m.Diagnose(context.Background(), "b.foo")
	if err != nil || !ok || len(lines) != 1 {
		t.Fatalf("after Reroot: lines=%q ok=%v err=%v", lines, ok, err)
	}
}
