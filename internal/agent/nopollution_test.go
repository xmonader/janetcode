package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEditUndoRewindCommitLeaveNothingOfOurs mirrors the invariant that
// janetcode leaves nothing in the repository: after edits, a rewind, an
// undo and a commit, the status (ignored files included) shows nothing
// janetcode made, and there is no new branch, tag or stash. The only refs
// it may add are its own, under refs/janetcode/.
func TestEditUndoRewindCommitLeaveNothingOfOurs(t *testing.T) {
	a, root, _, _ := loggedTurns(t, writeTurns("one", "two", "three"))
	ctx := context.Background()
	for _, p := range []string{"do one", "do two", "do three"} {
		if err := a.Run(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.PreviewUndoTurn(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, root, "status", "--porcelain", "--ignored"); got != "" {
		t.Fatalf("status after edit, rewind and undo:\n%s", got)
	}
	// The user commits what is left of the work; nothing of ours is in it.
	gitOut(t, root, "add", "-A")
	gitOut(t, root, "commit", "-q", "--allow-empty", "-m", "after")
	if got := gitOut(t, root, "status", "--porcelain", "--ignored"); got != "" {
		t.Fatalf("status after commit:\n%s", got)
	}
	if got := gitOut(t, root, "ls-files", "--", ".janetcode"); got != "" {
		t.Fatalf("a janetcode path is tracked: %s", got)
	}
	if got := gitOut(t, root, "stash", "list"); got != "" {
		t.Fatalf("stash: %s", got)
	}
	if got := gitOut(t, root, "tag", "--list"); got != "" {
		t.Fatalf("tags: %s", got)
	}
	for _, ref := range strings.Fields(gitOut(t, root, "for-each-ref", "--format=%(refname)")) {
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/janetcode/") {
			t.Errorf("unexpected ref %s", ref)
		}
	}
	if heads := strings.Fields(gitOut(t, root, "for-each-ref", "--format=%(refname)", "refs/heads")); len(heads) != 1 {
		t.Errorf("branches = %v, want only the one the repository began with", heads)
	}
}
