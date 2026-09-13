package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// setupUndoIntentAgent runs one turn that makes two unrelated changes — a
// logging change and a rename — so a description can be asked to tell
// them apart.
func setupUndoIntentAgent(t *testing.T) (*Agent, string, *fake) {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add debug logging output."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: []byte(`{"path":"logger.go","content":"package m\n\nfunc init() {\n\tlogging.Debug(\"start\")\n}\n"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename the Foo variable to Bar."},
			{Type: llm.BlockToolUse, ID: "c2", Name: "write_file", Input: []byte(`{"path":"app.go","content":"package m\n\nfunc run() {\n\tbar := 1\n\t_ = bar\n}\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(a.Changes()) != 2 {
		t.Fatalf("setup: changes = %+v", a.Changes())
	}
	return a, root, f
}

func TestSelectChangesByModelPicksSeqsAndDropsUnknown(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	f.responses = append(f.responses, text(`{"undo":[1,42],"keep":[2],"why":"matches the logging change"}`))
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("undo = %+v", sel.Undo)
	}
	if len(sel.Keep) != 1 || sel.Keep[0].Path != filepath.Join(root, "app.go") {
		t.Fatalf("keep = %+v", sel.Keep)
	}
	if sel.Why != "matches the logging change" {
		t.Fatalf("why = %q", sel.Why)
	}
	if sel.Fallback {
		t.Fatal("a model reply should not be reported as a fallback")
	}
}

func TestSelectChangesToleratesACodeFence(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	f.responses = append(f.responses, text("Sure, here you go:\n```json\n{\"undo\":[1],\"keep\":[2],\"why\":\"the logging one\"}\n```\n"))
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("undo = %+v", sel.Undo)
	}
}

func TestSelectChangesFallsBackToBM25WithoutAProvider(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	a.Provider = nil
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if !sel.Fallback {
		t.Fatal("expected Fallback when there is no provider")
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("keyword fallback should pick the logging change: undo = %+v", sel.Undo)
	}
	for _, c := range sel.Keep {
		if c.Path == filepath.Join(root, "logger.go") {
			t.Fatal("the logging change should not also be kept")
		}
	}
}

func TestSelectChangesOnParseFailureFallsBackToBM25(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	f.responses = append(f.responses, text("I'm not sure, sorry."))
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if !sel.Fallback {
		t.Fatal("an unparsable reply should fall back to keyword matching")
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("undo = %+v", sel.Undo)
	}
}

func TestSelectChangesNoCandidates(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	if _, err := a.SelectChanges(context.Background(), "anything"); !errors.Is(err, ErrNoChangesToUndo) {
		t.Fatalf("no changes: %v", err)
	}
}

func TestUndoSelectionRevertsNewestFirstStopsOnRefusalRunsChecks(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	changes := a.Changes() // [0]=seq1 logger.go, [1]=seq2 app.go
	// logger.go (the older change) is edited outside the session, so
	// undoing its creation will refuse; app.go (the newer one) is
	// untouched and should revert first and successfully.
	if err := os.WriteFile(filepath.Join(root, "logger.go"), []byte("changed externally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"true"}}}
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{changes[0], changes[1]}})
	if err != nil {
		t.Fatalf("UndoSelection: %v", err)
	}
	if len(rep.Reverted) != 1 || !strings.Contains(rep.Reverted[0], "app.go") {
		t.Fatalf("reverted = %+v", rep.Reverted)
	}
	if rep.Refused == "" || !strings.Contains(rep.Refused, "not reverted: #1") {
		t.Fatalf("refused = %q", rep.Refused)
	}
	if rep.ChecksOK == nil || !*rep.ChecksOK || rep.Checks == "" {
		t.Fatalf("checks = %q ok=%v", rep.Checks, rep.ChecksOK)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); !os.IsNotExist(err) {
		t.Fatalf("app.go should have been removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "logger.go")); err != nil {
		t.Fatalf("logger.go should have been left alone: %v", err)
	}
}

func TestUndoSelectionNothingSelected(t *testing.T) {
	a, _, _ := setupUndoIntentAgent(t)
	if _, err := a.UndoSelection(context.Background(), Selection{}); !errors.Is(err, ErrNothingSelected) {
		t.Fatalf("empty selection: %v", err)
	}
}

func TestPreviewAndApplyUndoAbout(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	f.responses = append(f.responses, text(`{"undo":[1],"keep":[2],"why":"matches the logging change"}`))
	sel, err := a.PreviewUndoAbout(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("preview = %+v", sel.Undo)
	}
	rep, err := a.ApplyPendingUndo(context.Background())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rep.Reverted) != 1 {
		t.Fatalf("reverted = %+v", rep.Reverted)
	}
	if _, err := os.Stat(filepath.Join(root, "logger.go")); !os.IsNotExist(err) {
		t.Fatalf("logger.go should have been removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); err != nil {
		t.Fatalf("app.go (kept) should still be there: %v", err)
	}
	// The pending selection is consumed by a successful apply.
	if _, err := a.ApplyPendingUndo(context.Background()); !errors.Is(err, ErrNoPendingUndo) {
		t.Fatalf("second apply: %v", err)
	}
}

func TestApplyPendingUndoRefusesAfterChangesMoved(t *testing.T) {
	a, _, f := setupUndoIntentAgent(t)
	f.responses = append(f.responses, text(`{"undo":[1],"keep":[2],"why":"the logging change"}`))
	if _, err := a.PreviewUndoAbout(context.Background(), "the logging change"); err != nil {
		t.Fatal(err)
	}
	// The session's changes moved since the preview: change #2 was
	// undone directly, so apply must not act on a stale selection.
	if _, err := a.UndoChange(2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(context.Background()); !errors.Is(err, ErrUndoSelectionStale) {
		t.Fatalf("apply after change: %v", err)
	}
}
