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
	start := time.Now()
	_, err := rf.Run(context.Background(), json.RawMessage(`{"path":"pipe"}`))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("read_file on a FIFO: err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the refusal took %s; it blocked instead of failing fast", time.Since(start))
	}
	wf := WriteFile{Root: root, WS: ws}
	if _, err := wf.Run(context.Background(), json.RawMessage(`{"path":"pipe","content":"x"}`)); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("write_file onto a FIFO: err = %v", err)
	}
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
