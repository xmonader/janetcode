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

// threeTurns runs three turns in a git repository: turn 1 writes one.txt,
// turn 2 writes two.txt and rewrites a.txt, turn 3 writes three.txt.
func threeTurns(t *testing.T) (*Agent, string) {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"one.txt","content":"one"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"two.txt","content":"two"}`),
		toolCall("t3", "write_file", `{"path":"a.txt","content":"rewritten"}`),
		text("done"),
		toolCall("t4", "write_file", `{"path":"three.txt","content":"three"}`),
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
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	for _, p := range []string{"one", "two", "three"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	return a, root
}

func TestRestoreToTurnPreviewsThenApplies(t *testing.T) {
	a, root := threeTurns(t)
	if got := a.RestorableTurns(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("restorable turns = %v", got)
	}
	tr, err := a.PreviewUndoTurn(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(tr.Lines, "\n")
	for _, want := range []string{"remove " + filepath.Join(root, "two.txt"), "remove " + filepath.Join(root, "three.txt"), "restore " + filepath.Join(root, "a.txt")} {
		if !strings.Contains(joined, want) {
			t.Errorf("preview missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "one.txt") {
		t.Fatalf("turn 1's work should survive a restore to before turn 2:\n%s", joined)
	}
	// A preview touches nothing.
	if _, err := os.Stat(filepath.Join(root, "three.txt")); err != nil {
		t.Fatal("the preview changed the tree")
	}
	if out := FormatTurnRestore(root, tr); !strings.Contains(out, "before turn 2") || !strings.Contains(out, "two.txt") {
		t.Fatalf("sheet:\n%s", out)
	}

	rep, err := a.ApplyPendingUndo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "two.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("two.txt survived the restore")
	}
	if _, err := os.Stat(filepath.Join(root, "three.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("three.txt survived the restore")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "one.txt")); string(data) != "one" {
		t.Fatalf("turn 1's file went too: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q, want its state before turn 2", data)
	}
	if !strings.Contains(strings.Join(rep.Reverted, "\n"), "two.txt") {
		t.Fatalf("report does not name what it restored: %+v", rep.Reverted)
	}

	// The restore is itself a change: it shows in /changes and /undo <n>
	// can take it back.
	var restored []Change
	for _, c := range a.Changes() {
		if c.Tool == "undo" && strings.Contains(c.Intent, "before turn 2") {
			restored = append(restored, c)
		}
	}
	if len(restored) < 3 {
		t.Fatalf("the restore recorded %d changes, want one per file it touched", len(restored))
	}
	// Including the one it removed: the change recorded the file as
	// edited to nothing, and /undo <n> refused with "the file is gone".
	for _, c := range restored {
		if c.Path != filepath.Join(root, "two.txt") {
			continue
		}
		if _, err := a.UndoChange(c.Seq); err != nil {
			t.Fatalf("/undo #%d of the removal: %v", c.Seq, err)
		}
		if data, err := os.ReadFile(c.Path); err != nil || string(data) != "two" {
			t.Fatalf("two.txt after undoing its removal = %q, %v", data, err)
		}
		if err := os.Remove(c.Path); err != nil {
			t.Fatal(err)
		}
	}
	// Turns 2 and 3 can no longer be restored to: the tree no longer
	// holds what they did.
	if got := a.RestorableTurns(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("restorable turns after the restore = %v", got)
	}
	// The conversation went back with the tree, and the note standing
	// where the dropped turns were names what changed. TellModelUndone
	// is still called the way a front-end calls it; it adds nothing on
	// top of the rewind's own note.
	a.TellModelUndone(rep)
	if rep.Rewound == nil || rep.Rewound.FromTurn != 2 {
		t.Fatalf("the restore did not rewind the conversation: %+v", rep.Rewound)
	}
	var told string
	for _, m := range a.Messages {
		if strings.Contains(m.Text(), "this conversation was rewound") {
			told = m.Text()
		}
	}
	if !strings.Contains(told, "two.txt") || !strings.Contains(told, "turn 2") {
		t.Fatalf("the model was not told what was restored: %q", told)
	}
	if strings.Contains(strings.Join(texts(a.Messages), "\n"), "reverted some of your earlier changes") {
		t.Fatal("a rewound restore also left the /undo apply note, so the model is told twice")
	}
}

func TestRestoreToTurnRefusesAGoneSnapshot(t *testing.T) {
	a, _ := threeTurns(t)
	_, err := a.PreviewUndoTurn(context.Background(), 9, false)
	if !errors.Is(err, ErrNoTurnCheckpoint) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "restorable turns: 1, 2, 3") {
		t.Fatalf("the refusal does not say what can be restored: %v", err)
	}
	// Trimming (or an /undo) takes a turn's snapshot away: the refusal is
	// the same, and nothing is applied.
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PreviewUndoTurn(context.Background(), 3, false); !errors.Is(err, ErrNoTurnCheckpoint) {
		t.Fatalf("a popped turn should no longer be restorable: %v", err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrNoPendingUndo) {
		t.Fatalf("a refused preview left something pending: %v", err)
	}
	// A session with no checkpoints at all says so plainly.
	b := &Agent{}
	if _, err := b.PreviewUndoTurn(context.Background(), 1, false); !errors.Is(err, ErrNothingToRestore) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreToTurnRefusesWhenTheRepositoryMoved(t *testing.T) {
	a, root := threeTurns(t)
	gitInit(t, root) // the user commits: HEAD moves under the snapshots
	_, err := a.PreviewUndoTurn(context.Background(), 2, false)
	if !errors.Is(err, snapshot.ErrHeadMoved) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "turn 2") {
		t.Fatalf("the refusal does not name the turn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "three.txt")); err != nil {
		t.Fatal("a refused restore changed the tree")
	}
}

func TestRestoreToTurnRefusesOnceTheTurnsMoved(t *testing.T) {
	a, _ := threeTurns(t)
	if _, err := a.PreviewUndoTurn(context.Background(), 2, false); err != nil {
		t.Fatal(err)
	}
	// Another turn's worth of checkpoints lands between the preview and
	// the apply: what was listed is no longer what would happen.
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrUndoSelectionStale) {
		t.Fatalf("err = %v", err)
	}
}

// TestChangesMarksTheTurnsYouCanRestoreTo: /changes says which turn
// "/undo turn n" can put the work tree back to.
func TestChangesMarksTheTurnsYouCanRestoreTo(t *testing.T) {
	a, root := threeTurns(t)
	out := FormatChangeGroups(a.ChangeGroups(), root, a.RestorableTurns())
	if !strings.Contains(out, "turn 2 [restorable]") {
		t.Fatalf("/changes does not mark the restorable turns:\n%s", out)
	}
	if !strings.Contains(out, "/undo turn <n>") {
		t.Fatalf("/changes does not say what the mark means:\n%s", out)
	}
	if plain := FormatChangeGroups(a.ChangeGroups(), root, nil); strings.Contains(plain, "restorable") {
		t.Fatalf("nothing restorable should leave the listing alone:\n%s", plain)
	}
}

// TestRestoreToTurnOutsideGitUsesTheFileCopies: with no repository there
// is no work-tree snapshot, so the restore falls back to the copies the
// file tools made — the same fallback /undo uses.
func TestRestoreToTurnOutsideGitUsesTheFileCopies(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"first"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"x","content":"second"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	for _, p := range []string{"one", "two"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.PreviewUndoTurn(context.Background(), 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("x should be gone: it did not exist before turn 1")
	}
}

// A restore that puts some files back and then fails on another used to
// return the error with nothing recorded: the files it did put back were
// not a change /undo <n> could take back, and nothing said they had
// moved. What was actually restored is now recorded, and the turn's
// checkpoint is kept, so the restore can be retried or taken back.
func TestRestoreToTurnThatFailsPartWayRecordsWhatItRestored(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"changed"}`),
		toolCall("t2", "write_file", `{"path":"y","content":"new"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	// y cannot be removed any more: it is a directory with a file in it.
	y := filepath.Join(root, "y")
	if err := os.Remove(y); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(y, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PreviewUndoTurn(context.Background(), 1, false); err != nil {
		t.Fatal(err)
	}
	before := len(a.Changes())
	// Forced: swapping y for a directory is itself an edit since the turn.
	if _, err := a.ApplyPendingUndo(context.Background(), true); err == nil {
		t.Fatal("the restore could not remove y, so it should report an error")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q; setup expected it restored before the failure", data)
	}
	var recorded []string
	for _, c := range a.Changes()[before:] {
		if c.Tool == "undo" {
			recorded = append(recorded, filepath.Base(c.Path))
		}
	}
	if len(recorded) != 1 || recorded[0] != "a.txt" {
		t.Fatalf("recorded undo changes = %v, want just a.txt", recorded)
	}
	if got := a.RestorableTurns(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("restorable turns = %v; the partial restore should keep turn 1 to retry", got)
	}
}

// texts is every message's text, for a test asserting something is not
// anywhere in the conversation.
func texts(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Text())
	}
	return out
}
