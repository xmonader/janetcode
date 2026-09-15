package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

func headOf(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestFirstTurnRecordsStartOnce checks the half of "real sessions become
// evals" that lives in the agent: the very first user turn of a session in
// a git repository records HEAD and a snapshot of the work tree once, and
// a later turn does not touch it again even though the tree has since
// changed underneath it.
func TestFirstTurnRecordsStartOnce(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok"), text("ok again")}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess

	if err := a.Run(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if sess.Meta.Start == nil {
		t.Fatal("Start was not recorded on the first turn")
	}
	wantHead := headOf(t, root)
	if sess.Meta.Start.Head != wantHead {
		t.Fatalf("Start.Head = %s, want %s", sess.Meta.Start.Head, wantHead)
	}
	if sess.Meta.Start.Snap == "" {
		t.Fatal("Start.Snap is empty")
	}
	firstSnap := sess.Meta.Start.Snap

	// The tree changes and a second turn runs; Start must not move.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	if sess.Meta.Start.Snap != firstSnap {
		t.Fatalf("Start was recorded again: got %s, want %s", sess.Meta.Start.Snap, firstSnap)
	}
}

// TestFirstTurnRecordsNothingOutsideGit is the non-git and no-log paths:
// recordStart must not panic or block a turn when there is nothing to
// snapshot.
func TestFirstTurnRecordsNothingOutsideGit(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok")}}
	a, _ := newAgent(t, f, nil) // no Snapshots, no Log
	if err := a.Run(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
}
