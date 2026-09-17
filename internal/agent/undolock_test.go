package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/snapshot"
)

// blockingSnaps is a Snapshotter whose Restore blocks until it is
// released, so a test can look at the agent while an undo is half done.
type blockingSnaps struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSnaps) Take(context.Context, string) (snapshot.Snap, error) {
	return snapshot.Snap{Commit: "taken"}, nil
}

func (b *blockingSnaps) Restore(context.Context, snapshot.Snap) ([]string, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return []string{"restored a.go"}, nil
}

func (b *blockingSnaps) RestoreSkip(_ context.Context, s snapshot.Snap, _ map[string]bool) ([]string, error) {
	return b.Restore(context.Background(), s)
}

func (b *blockingSnaps) Drop(context.Context, snapshot.Snap)                   {}
func (b *blockingSnaps) Changed(context.Context, snapshot.Snap) (bool, error)  { return true, nil }
func (b *blockingSnaps) Diff(context.Context, snapshot.Snap) ([]string, error) { return nil, nil }
func (b *blockingSnaps) Moved(context.Context, snapshot.Snap) (bool, error)    { return false, nil }

// TestUndoDoesNotHoldLockAcrossGit: the git restore an /undo runs can take
// up to a minute, and it used to run with cp.mu held, so every front-end
// read of the checkpoints or the change log — the status bar's, /changes,
// /blame — stalled behind it. The lock now covers only the bookkeeping at
// either end.
func TestUndoDoesNotHoldLockAcrossGit(t *testing.T) {
	snap := snapshot.Snap{Commit: "before"}
	repo := &blockingSnaps{entered: make(chan struct{}), release: make(chan struct{})}
	a := &Agent{}
	a.snapshotter = repo
	a.cp.all = []Checkpoint{{Turn: 1, Snap: &snap}}
	a.cp.changes = []Change{{Seq: 1, Path: "a.go"}}

	done := make(chan error, 1)
	go func() {
		_, err := a.Undo(context.Background())
		done <- err
	}()
	<-repo.entered

	read := make(chan int, 1)
	go func() { read <- len(a.Checkpoints()) + len(a.Changes()) }()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		close(repo.release)
		t.Fatal("Checkpoints/Changes blocked while an undo was inside git")
	}

	close(repo.release)
	if err := <-done; err != nil {
		t.Fatalf("undo: %v", err)
	}
	if n := len(a.Checkpoints()); n != 0 {
		t.Fatalf("the undone checkpoint should be gone, got %d", n)
	}
	// The state the undo replaced is reachable again through Redo.
	if _, err := a.Redo(context.Background()); err != nil {
		t.Fatalf("redo: %v", err)
	}
}
