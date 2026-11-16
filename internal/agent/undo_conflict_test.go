package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// A restore refuses to overwrite a file edited after the turns it undoes
// until the user forces it, and says which files and why, in the preview
// and again at apply time (the tree may move between the two).
func TestRestoreToTurnRefusesEditedFilesUnlessForced(t *testing.T) {
	a, root := threeTurns(t)
	ctx := context.Background()
	three := filepath.Join(root, "three.txt")
	mine := filepath.Join(root, "mine.txt")
	if err := os.WriteFile(three, []byte("three, edited by hand"), 0o644); err != nil { // a turn's file, edited since
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("not from any turn"), 0o644); err != nil { // untracked, no turn's
		t.Fatal(err)
	}
	tr, err := a.PreviewUndoTurn(ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range tr.Conflicts {
		names = append(names, filepath.Base(c.Path))
	}
	if len(names) != 2 {
		t.Fatalf("conflicts = %v, want three.txt and mine.txt", names)
	}
	if sheet := FormatTurnRestore(root, tr); !strings.Contains(sheet, "CONFLICT") || !strings.Contains(sheet, "--force") || !strings.Contains(sheet, "three.txt") {
		t.Fatalf("the preview must name the conflicts and the way past them:\n%s", sheet)
	}
	_, err = a.ApplyPendingUndo(ctx, false)
	if !errors.Is(err, ErrTurnRestoreConflict) || !strings.Contains(err.Error(), "mine.txt") {
		t.Fatalf("apply = %v, want a refusal naming the files", err)
	}
	for p, want := range map[string]string{three: "three, edited by hand", mine: "not from any turn"} {
		if data, _ := os.ReadFile(p); string(data) != want {
			t.Fatalf("a refused restore changed %s: %q", p, data)
		}
	}
	// The refusal keeps the preview, so the same apply can be forced.
	rep, err := a.ApplyPendingUndo(ctx, true)
	if err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	if _, err := os.Stat(three); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("force did not restore three.txt")
	}
	if !strings.Contains(strings.Join(rep.Reverted, "\n"), "forced over ") || !strings.Contains(strings.Join(rep.Reverted, "\n"), "three.txt: it was modified after the turn") {
		t.Fatalf("the report must say what was forced: %v", rep.Reverted)
	}
}

// A file edited between the preview and the apply is caught at apply.
func TestRestoreToTurnChecksConflictsAgainAtApply(t *testing.T) {
	a, root := threeTurns(t)
	ctx := context.Background()
	if tr, err := a.PreviewUndoTurn(ctx, 3, false); err != nil || len(tr.Conflicts) != 0 {
		t.Fatalf("a clean tree has no conflicts: %+v, %v", tr.Conflicts, err)
	}
	if err := os.WriteFile(filepath.Join(root, "three.txt"), []byte("edited after the preview"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(ctx, false); !errors.Is(err, ErrTurnRestoreConflict) {
		t.Fatalf("apply = %v, want a conflict refusal", err)
	}
}

// Deletions: a file a turn deleted restores without --force on a clean
// tree, and is a conflict once something recreated it.
func TestRestoreToTurnDeletedFileRecreatedIsAConflict(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm a.txt"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "rm", nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if tr, err := a.PreviewUndoTurn(ctx, 1, false); err != nil || len(tr.Conflicts) != 0 {
		t.Fatalf("restoring a deletion on a clean tree: %+v, %v", tr.Conflicts, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("recreated"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := a.PreviewUndoTurn(ctx, 1, false)
	if err != nil || len(tr.Conflicts) != 1 || !strings.Contains(tr.Conflicts[0].Why, "exists again") {
		t.Fatalf("conflicts = %+v, %v", tr.Conflicts, err)
	}
}
