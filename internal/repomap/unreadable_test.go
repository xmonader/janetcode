package repomap

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// One file the index cannot read — permissions, a directory or a FIFO
// with a source extension — is skipped; it used to fail the whole map, or
// in the FIFO's case hang Build forever on a read that never ends.
func TestBuildSkipsFilesItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs or chmod 000")
	}
	root := fixture(t)
	locked := filepath.Join(root, "locked.go")
	if err := os.WriteFile(locked, []byte("package m\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Log("running as a user who can read mode 000; the permission case is not exercised")
	}
	if err := os.Mkdir(filepath.Join(root, "odd.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var ix *Index
	go func() {
		var err error
		ix, err = Build(context.Background(), root, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Build failed on one unreadable file: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Build hung reading a FIFO")
	}
	if _, ok := ix.byPath["store/store.go"]; !ok {
		t.Fatalf("the readable files were not indexed: %v", paths(ix.Files))
	}
}

// The fallback walk honours the context: TurnPrep bounds the build to
// twenty seconds, and a tree outside version control (a home directory,
// say) used to be walked to the end regardless.
func TestBuildStopsOnACancelledContext(t *testing.T) {
	root := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, root, nil); err == nil {
		t.Fatal("Build walked the whole tree for a cancelled caller")
	}
}
