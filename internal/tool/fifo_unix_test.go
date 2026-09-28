//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mustRefuse runs one tool call and requires it to refuse rather than
// block. An open on a FIFO cannot be cancelled — os.ReadFile does not
// take a context — so the call runs on a goroutine of its own and the
// test reports the block. Asserting on elapsed time after the fact only
// works if the call returns at all: without the guard it never does, and
// the whole package used to fail on the go test timeout instead, naming
// the wrong thing.
func mustRefuse(t *testing.T, what string, run func() (string, error)) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() { out, err := run(); done <- result{out, err} }()
	select {
	case r := <-done:
		if r.err == nil || !strings.Contains(r.err.Error(), "not a regular file") {
			t.Fatalf("%s: err = %v, out = %q", what, r.err, r.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: blocked instead of being refused", what)
	}
}

// TestFileToolsRefuseSpecialFiles: a FIFO at the final component would
// block a read until some other process writes — hanging the tool for as
// long as the outer timeout, on a path the model chose. The guard refuses
// non-regular files before the open, fast.
func TestFileToolsRefuseSpecialFiles(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no mkfifo:", err)
	}
	ws := NewLocal(root)
	rf := ReadFile{Root: root, WS: ws}
	mustRefuse(t, "read_file on a FIFO", func() (string, error) {
		return rf.Run(context.Background(), json.RawMessage(`{"path":"pipe"}`))
	})
	wf := WriteFile{Root: root, WS: ws}
	mustRefuse(t, "write_file onto a FIFO", func() (string, error) {
		return wf.Run(context.Background(), json.RawMessage(`{"path":"pipe","content":"x"}`))
	})
	// A symlink to a regular file still reads: confinement, not the guard,
	// owns symlink policy, and in-workspace links are legitimate.
	regular := filepath.Join(root, "real.txt")
	if err := os.WriteFile(regular, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	out, err := rf.Run(context.Background(), json.RawMessage(`{"path":"link.txt"}`))
	if err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("read through an in-workspace symlink: %q %v", out, err)
	}
}

// TestSpecialFileGuardFollowsSymlinks: exempting symlinks outright and
// leaving them to the confinement check was a hole — confinement asks
// whether the target is inside the workspace, and an in-workspace FIFO
// is, so link -> pipe passed the guard and the read blocked exactly as a
// bare FIFO would. The guard was tested against link -> regular only,
// which certified the exempt branch without probing it.
func TestSpecialFileGuardFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("no mkfifo:", err)
	}
	if err := os.Symlink("pipe", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ws   *Workspace
	}{
		// A nil workspace is not a test fixture: subagent worktrees
		// (agent/worktree.go) and minimal mode are both wired that way,
		// and the guard lived only on the Local host, so both reached
		// the os with nothing in the way.
		{"local host", NewLocal(root)},
		{"no workspace", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rf := ReadFile{Root: root, WS: tc.ws}
			mustRefuse(t, "read_file through a symlink to a FIFO", func() (string, error) {
				return rf.Run(context.Background(), json.RawMessage(`{"path":"link"}`))
			})
			wf := WriteFile{Root: root, WS: tc.ws}
			mustRefuse(t, "write_file through a symlink to a FIFO", func() (string, error) {
				return wf.Run(context.Background(), json.RawMessage(`{"path":"link","content":"x"}`))
			})
		})
	}
}

// TestNoWorkspaceStillRefusesAFifo is the same gap on a bare FIFO: the
// guard was on the host, and these wirings have no host.
func TestNoWorkspaceStillRefusesAFifo(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("no mkfifo:", err)
	}
	rf := ReadFile{Root: root}
	mustRefuse(t, "read_file on a FIFO with no workspace", func() (string, error) {
		return rf.Run(context.Background(), json.RawMessage(`{"path":"pipe"}`))
	})
}

// list_dir opens the path it is given to read its entries, and opening a
// FIFO blocks until a writer comes along — the directory read never got
// as far as saying "not a directory". A path that is not a directory is
// now refused on its stat.
func TestListDirRefusesAFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("no mkfifo:", err)
	}
	for _, ws := range []*Workspace{nil, NewLocal(root)} {
		done := make(chan error, 1)
		go func() {
			_, err := (ListDir{Root: root, WS: ws}).Run(context.Background(), json.RawMessage(`{"path":"pipe"}`))
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Fatalf("list_dir on a FIFO: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("list_dir on a FIFO blocked instead of being refused")
		}
	}
}
