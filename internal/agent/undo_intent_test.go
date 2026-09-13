package agent

import (
	"context"
	"errors"
	"fmt"
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
	rep, err := a.ApplyPendingUndo(context.Background(), false)
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
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrNoPendingUndo) {
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
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrUndoSelectionStale) {
		t.Fatalf("apply after change: %v", err)
	}
}

// setupRenameCouplingAgent recreates the QA scenario for the coupling
// guard: a rename (compute -> calculate) is recorded across two changes —
// one edit_file call to main.go that both renames the function AND tweaks
// an unrelated log message, and a second, separate write_file call
// creating main_test.go, which already calls the new name. The rename's
// def and call site, and the log edit, are kept far enough apart in
// main.go that they land in separate diff hunks, matching how renames()
// expects one hunk to carry one, unambiguous, renamed identifier.
func setupRenameCouplingAgent(t *testing.T) (a *Agent, root string, f *fake, renameSeq, testSeq int) {
	t.Helper()
	mainGo := "package m\n\nfunc compute() int {\n\treturn 1\n}\n\n" +
		"// spacer1\n// spacer2\n// spacer3\n// spacer4\n// spacer5\n// spacer6\n// spacer7\n\n" +
		"func run() {\n\tx := compute()\n\tlog.Println(\"starting\")\n\t_ = x\n}\n"
	f = &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Create main.go."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: mustJSON(map[string]any{"path": "main.go", "content": mainGo})},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename compute to calculate; also fix the log wording."},
			{Type: llm.BlockToolUse, ID: "c2", Name: "edit_file", Input: mustJSON(map[string]any{
				"path": "main.go",
				"edits": []map[string]any{
					{"old_string": "compute", "new_string": "calculate", "replace_all": true},
					{"old_string": "starting", "new_string": "started"},
				},
			})},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add a test calling calculate."},
			{Type: llm.BlockToolUse, ID: "c3", Name: "write_file", Input: mustJSON(map[string]any{
				"path":    "main_test.go",
				"content": "package m\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {\n\tif calculate() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
			})},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root = newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 3 {
		t.Fatalf("setup: changes = %+v", changes)
	}
	return a, root, f, changes[1].Seq, changes[2].Seq
}

// TestDetectCouplingWarnsWhenARenameIsSplitAcrossUndoAndKeep is QA
// scenario (1): the rename is recorded across two changes, one of which
// (main.go) also carries an unrelated log edit that matches the undo
// description. Undoing only that change would leave main_test.go calling
// a name main.go no longer defines; the deterministic guard must catch
// this whether or not the model itself notices.
func TestDetectCouplingWarnsWhenARenameIsSplitAcrossUndoAndKeep(t *testing.T) {
	a, root, f, renameSeq, testSeq := setupRenameCouplingAgent(t)
	f.responses = append(f.responses, text(fmt.Sprintf(
		`{"undo":[%d],"keep":[%d],"mixed":[],"why":"matches the log message wording"}`, renameSeq, testSeq)))
	sel, err := a.PreviewUndoAbout(context.Background(), "the log message wording")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Warnings) == 0 {
		t.Fatalf("expected a coupling warning, got none: sel=%+v", sel)
	}
	want := fmt.Sprintf("#%d and #%d", renameSeq, testSeq)
	if !strings.Contains(sel.Warnings[0], want) || !strings.Contains(sel.Warnings[0], "compute") || !strings.Contains(sel.Warnings[0], "calculate") {
		t.Fatalf("warning = %q", sel.Warnings[0])
	}
	if rendered := FormatSelection(root, sel); !strings.Contains(rendered, "warning:") || !strings.Contains(rendered, "--force") {
		t.Fatalf("FormatSelection did not surface the warning:\n%s", rendered)
	}
	// Refused without --force...
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrUndoNeedsForce) {
		t.Fatalf("apply without force: %v", err)
	}
	// ...and the preview survives the refusal so --force can act on it
	// without asking the model again.
	rep, err := a.ApplyPendingUndo(context.Background(), true)
	if err != nil {
		t.Fatalf("apply with force: %v", err)
	}
	if len(rep.Reverted) != 1 {
		t.Fatalf("reverted = %+v", rep.Reverted)
	}
}

// TestSelectionMixedChangeBlocksApplyWithoutForce is QA scenario (2): a
// single edit_file call mixes an edit matching the description with one
// that does not. A fake provider reports it as mixed rather than picking
// a side, and apply must refuse it short of --force.
func TestSelectionMixedChangeBlocksApplyWithoutForce(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	changes := a.Changes()
	mixedSeq := changes[0].Seq
	f.responses = append(f.responses, text(fmt.Sprintf(
		`{"undo":[],"keep":[],"mixed":[{"seq":%d,"why":"mixes the logging edit with an unrelated change"}],"why":"could not cleanly split #%d"}`,
		mixedSeq, mixedSeq)))
	sel, err := a.PreviewUndoAbout(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Mixed) != 1 || sel.Mixed[0].Change.Seq != mixedSeq {
		t.Fatalf("mixed = %+v", sel.Mixed)
	}
	if sel.Mixed[0].Why == "" {
		t.Fatal("expected a reason for the mixed change")
	}
	// The mixed change must not silently end up on either side.
	for _, c := range sel.Undo {
		if c.Seq == mixedSeq {
			t.Fatal("a mixed change must not also be in Undo")
		}
	}
	for _, c := range sel.Keep {
		if c.Seq == mixedSeq {
			t.Fatal("a mixed change must not also be in Keep")
		}
	}
	if rendered := FormatSelection(root, sel); !strings.Contains(rendered, "mixed:") || !strings.Contains(rendered, "--force") {
		t.Fatalf("FormatSelection did not surface the mixed change:\n%s", rendered)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrUndoNeedsForce) {
		t.Fatalf("apply without force: %v", err)
	}
}

// TestUndoReportSaysWhenVerifyIsNotTrusted is QA defect A: an untrusted
// project verify config must not leave the report silent.
func TestUndoReportSaysWhenVerifyIsNotTrusted(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	a.VerifyUntrusted = true
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{a.Changes()[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checks != "" {
		t.Fatalf("Checks should be empty when verify is untrusted: %q", rep.Checks)
	}
	if !strings.Contains(rep.CheckNote, verify.ProjectFile) || !strings.Contains(rep.CheckNote, "not trusted") || !strings.Contains(rep.CheckNote, "/verify trust") {
		t.Fatalf("CheckNote = %q", rep.CheckNote)
	}
	if !strings.Contains(FormatUndoReport(rep), "not trusted") {
		t.Fatalf("FormatUndoReport did not mention the untrusted config:\n%s", FormatUndoReport(rep))
	}
	_ = root
}

// TestUndoReportSaysWhenNoVerifyIsConfigured is QA defect A's other half:
// no project verify config at all is a distinct message from "not
// trusted", and with no diagnose hook either, the report says plainly
// that nothing verified the result.
func TestUndoReportSaysWhenNoVerifyIsConfigured(t *testing.T) {
	a, _, _ := setupUndoIntentAgent(t)
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{a.Changes()[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.CheckNote, "no checks configured") || !strings.Contains(rep.CheckNote, "/verify set") {
		t.Fatalf("CheckNote = %q", rep.CheckNote)
	}
	if !strings.Contains(rep.Diagnostics, "nothing verified the result") {
		t.Fatalf("Diagnostics = %q", rep.Diagnostics)
	}
}

// TestUndoReportFallsBackToLSPDiagnostics is QA defect A's LSP fallback:
// with no verify gate at all, a fake diagnose hook stands in for the
// language server and its reported error must reach the report.
func TestUndoReportFallsBackToLSPDiagnostics(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	var asked []string
	a.Diagnose = func(_ context.Context, path string) string {
		asked = append(asked, path)
		return "[diagnostics] app.go:3: undefined: bar"
	}
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{a.Changes()[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != filepath.Join(root, "app.go") {
		t.Fatalf("diagnose asked about = %+v", asked)
	}
	if !strings.Contains(rep.Diagnostics, "undefined: bar") {
		t.Fatalf("Diagnostics = %q", rep.Diagnostics)
	}
	if out := FormatUndoReport(rep); !strings.Contains(out, "undefined: bar") {
		t.Fatalf("FormatUndoReport did not include the diagnostics:\n%s", out)
	}
}

func TestParseUndoArgs(t *testing.T) {
	cases := []struct {
		rest string
		want UndoArgs
	}{
		{"", UndoArgs{}},
		{"preview", UndoArgs{Preview: true}},
		{"apply", UndoArgs{Apply: true}},
		{"apply --force", UndoArgs{Apply: true, Force: true}},
		{"about the logging change", UndoArgs{About: true, Description: "the logging change"}},
		{"about", UndoArgs{About: true}},
		{"3", UndoArgs{N: 3}},
	}
	for _, c := range cases {
		got, err := ParseUndoArgs(c.rest)
		if err != nil {
			t.Fatalf("%q: %v", c.rest, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %+v want %+v", c.rest, got, c.want)
		}
	}
	for _, bad := range []string{"apply --now", "preview extra", "nonsense"} {
		if _, err := ParseUndoArgs(bad); err == nil {
			t.Fatalf("%q: expected an error", bad)
		}
	}
}
