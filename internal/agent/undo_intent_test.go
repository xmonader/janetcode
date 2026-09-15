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
// main.go that they land in separate diff hunks — a gentler case than
// setupRealisticRenameCouplingAgent's, where they land in the very same
// hunk, but useful for checking the guard also catches a rename that
// spans hunks (a definition in one, a call site in another).
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
	if want := fmt.Sprintf("#%d uses calculate", testSeq); !strings.Contains(sel.Warnings[0], want) {
		t.Fatalf("warning = %q, want it to name the kept change and the identifier: %q", sel.Warnings[0], want)
	}
	if want := fmt.Sprintf("undoing #%d", renameSeq); !strings.Contains(sel.Warnings[0], want) {
		t.Fatalf("warning = %q, want it to name the undone change: %q", sel.Warnings[0], want)
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

// setupRealisticRenameCouplingAgent reproduces the real end-to-end failure
// (reproduced twice with a real model, not this test's fake): a Go module
// where main.go and main_test.go already exist on disk before the session
// starts. One model turn adds log.Printf lines to compute() and main() AND
// renames compute -> calculate everywhere. Unlike
// setupRenameCouplingAgent's carefully spaced edits, a real model's rename
// and its nearby logging lines land close enough together that they fall
// in the SAME diff hunk — main.go ends up as one recorded change carrying
// both the rename and the logging, and main_test.go as a second change
// renaming only the call site.
func setupRealisticRenameCouplingAgent(t *testing.T) (a *Agent, root string, f *fake, mainSeq, testSeq int) {
	t.Helper()
	mainGoBefore := "package main\n\nimport \"fmt\"\n\n" +
		"func compute(x int) int {\n\treturn x * 2\n}\n\n" +
		"func main() {\n\tresult := compute(3)\n\tfmt.Println(result)\n}\n"
	mainGoAfter := "package main\n\nimport (\n\t\"fmt\"\n\t\"log\"\n)\n\n" +
		"func calculate(x int) int {\n\tlog.Printf(\"calculate start\")\n\treturn x * 2\n}\n\n" +
		"func main() {\n\tresult := calculate(3)\n\tlog.Printf(\"done\")\n\tfmt.Println(result)\n}\n"
	testGoBefore := "package main\n\nimport \"testing\"\n\n" +
		"func TestCompute(t *testing.T) {\n\tif compute(3) != 6 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"
	testGoAfter := "package main\n\nimport \"testing\"\n\n" +
		"func TestCompute(t *testing.T) {\n\tif calculate(3) != 6 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"

	f = &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename compute to calculate and add some logging."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: mustJSON(map[string]any{"path": "main.go", "content": mainGoAfter})},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Update the test to call the renamed function."},
			{Type: llm.BlockToolUse, ID: "c2", Name: "write_file", Input: mustJSON(map[string]any{"path": "main_test.go", "content": testGoAfter})},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root = newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainGoBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte(testGoBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 2 {
		t.Fatalf("setup: changes = %+v", changes)
	}
	// Confirm the premise: main.go's rename and its logging land in one
	// hunk, so a hunk-level "exactly one identifier added" heuristic
	// cannot isolate the rename here.
	hunks := changes[0].Hunks()
	if len(hunks) != 1 {
		t.Fatalf("setup: expected the rename and the logging to merge into one hunk, got %d", len(hunks))
	}
	return a, root, f, changes[0].Seq, changes[1].Seq
}

// TestUndoAboutCatchesRenameCoupledWithUnrelatedLoggingInOneHunk is the
// real end-to-end failure: the model is asked to undo "the logging changes
// but keep the rename", picks #1 (main.go) to undo and #2 (main_test.go) to
// keep with no mixed entry, and the old hunk-level detectCoupling missed it
// because main.go's single hunk mixes the rename with unrelated added
// identifiers (log, Printf, done, ...), so the "exactly one identifier
// added" heuristic never recognizes compute->calculate as a rename at all.
// Applying this selection would leave main_test.go calling calculate while
// main.go no longer defines it. The guard must catch this regardless of
// whether it can name it a "rename".
func TestUndoAboutCatchesRenameCoupledWithUnrelatedLoggingInOneHunk(t *testing.T) {
	a, root, f, mainSeq, testSeq := setupRealisticRenameCouplingAgent(t)
	f.responses = append(f.responses, text(fmt.Sprintf(`{"undo":[%d],"keep":[%d]}`, mainSeq, testSeq)))
	sel, err := a.PreviewUndoAbout(context.Background(), "the logging changes but keep the rename")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Mixed) != 0 {
		t.Fatalf("expected no mixed entry (matching the real repro), got %+v", sel.Mixed)
	}
	if len(sel.Warnings) == 0 {
		t.Fatalf("expected a coupling warning: undoing #%d would leave #%d calling a name main.go no longer defines, got none: sel=%+v", mainSeq, testSeq, sel)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrUndoNeedsForce) {
		t.Fatalf("apply without force: %v", err)
	}
	rep, err := a.ApplyPendingUndo(context.Background(), true)
	if err != nil {
		t.Fatalf("apply with force: %v", err)
	}
	if len(rep.Reverted) != 1 {
		t.Fatalf("reverted = %+v", rep.Reverted)
	}
	_ = root
}

// TestUndoAboutBothUndoneRaisesNoCouplingWarning is the other half of the
// same scenario: when the model (or the user) decides to undo both the
// rename and the logging together, nothing is left depending on a name
// that is about to disappear, so the guard must stay quiet.
func TestUndoAboutBothUndoneRaisesNoCouplingWarning(t *testing.T) {
	a, _, f, mainSeq, testSeq := setupRealisticRenameCouplingAgent(t)
	f.responses = append(f.responses, text(fmt.Sprintf(`{"undo":[%d,%d],"keep":[]}`, mainSeq, testSeq)))
	sel, err := a.PreviewUndoAbout(context.Background(), "undo the rename and the logging both")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Warnings) != 0 {
		t.Fatalf("expected no coupling warning when both changes are undone together, got %+v", sel.Warnings)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatalf("apply without force should not be refused: %v", err)
	}
}

// TestSelectChangesExcludesExternallyRevertedChangeAsUnavailable is item 3
// of the QA re-test: a change whose file was reverted outside the session
// (e.g. `git checkout`, here simulated by writing the pre-change content
// straight back) can no longer be located, so it must never be offered to
// the model or the keyword fallback as something to undo or to keep —
// either answer would promise a revert this guard cannot actually perform.
func TestSelectChangesExcludesExternallyRevertedChangeAsUnavailable(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	changes := a.Changes() // [0]=logger.go (created), [1]=app.go (created)
	// Simulate `git checkout -- logger.go` (or any external revert) after
	// the session made its change: the file goes back to not existing,
	// since that change created it, so its recorded After no longer
	// matches disk.
	if err := os.Remove(filepath.Join(root, "logger.go")); err != nil {
		t.Fatal(err)
	}
	f.responses = append(f.responses, text(fmt.Sprintf(`{"undo":[%d],"keep":[]}`, changes[1].Seq)))
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Unavailable) != 1 || sel.Unavailable[0].Change.Seq != changes[0].Seq {
		t.Fatalf("unavailable = %+v", sel.Unavailable)
	}
	for _, c := range append(append([]Change(nil), sel.Undo...), sel.Keep...) {
		if c.Seq == changes[0].Seq {
			t.Fatalf("the unavailable change must not also appear in undo or keep: %+v", sel)
		}
	}
	// The model was only ever shown the one remaining, available
	// candidate.
	if len(f.requests) == 0 {
		t.Fatal("expected the model to have been asked")
	}
	if prompt := string(f.requests[len(f.requests)-1].Messages[0].Text()); strings.Contains(prompt, "logger.go") {
		t.Fatalf("the unavailable change's file should not have been offered to the model:\n%s", prompt)
	}
	if rendered := FormatSelection(root, sel); !strings.Contains(rendered, "unavailable:") {
		t.Fatalf("FormatSelection did not surface the unavailable change:\n%s", rendered)
	}
}

// TestReportChecksDiagnosticsCoverKeptFilesToo is item 4 of the QA re-test:
// the language-server fallback must look at every file any candidate
// change touched, kept ones included, not only the ones actually
// reverted — otherwise a kept file that breaks because of what was
// undone is never examined, and the report falsely reads as clean.
func TestReportChecksDiagnosticsCoverKeptFilesToo(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	changes := a.Changes() // [0]=logger.go, [1]=app.go
	var asked []string
	a.Diagnose = func(_ context.Context, path string) string {
		asked = append(asked, path)
		if strings.Contains(path, "app.go") {
			return "" // no language server covers this one
		}
		return "[diagnostics] no problems"
	}
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{changes[0]}, Keep: []Change{changes[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatalf("expected both the reverted and the kept file to be checked, asked = %+v", asked)
	}
	if !strings.Contains(rep.Diagnostics, "logger.go") || !strings.Contains(rep.Diagnostics, "app.go") {
		t.Fatalf("Diagnostics should name every touched file: %q", rep.Diagnostics)
	}
	if !strings.Contains(rep.Diagnostics, "not checked") {
		t.Fatalf("the kept file with no language server coverage must say it was not checked, not go unmentioned: %q", rep.Diagnostics)
	}
	_ = root
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
		{"turn 4", UndoArgs{Turn: 4}},
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
	for _, bad := range []string{"apply --now", "preview extra", "nonsense", "turn", "turn x", "turn 0"} {
		if _, err := ParseUndoArgs(bad); err == nil {
			t.Fatalf("%q: expected an error", bad)
		}
	}
}

// TestSelectChangesAllUnavailableIsNotNothingToUndo: when every change
// the session made was reverted outside it, the preview names them as
// unavailable instead of claiming the session has no changes.
func TestSelectChangesAllUnavailableIsNotNothingToUndo(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	for _, name := range []string{"logger.go", "app.go"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatalf("err = %v, want a selection listing unavailable changes", err)
	}
	if len(sel.Unavailable) != 2 || len(sel.Undo) != 0 {
		t.Fatalf("selection = %+v", sel)
	}
	if rendered := FormatSelection(root, sel); !strings.Contains(rendered, "unavailable:") {
		t.Fatalf("unavailable changes not shown:\n%s", rendered)
	}
}

// The note telling the model what /undo apply reverted is the harness's,
// not the user's: its own synthetic block, with paths relative to the
// session root, so it neither runs into the next prompt ("...stayed.Re-add
// a TestClipBytes...") nor spells out the machine's directories.
func TestTellModelUndoneIsASeparateHarnessNoteWithRelativePaths(t *testing.T) {
	a, root, _ := setupUndoIntentAgent(t)
	a.Root = root
	rep, err := a.UndoSelection(context.Background(), Selection{Undo: []Change{a.Changes()[1]}})
	if err != nil {
		t.Fatal(err)
	}
	a.TellModelUndone(rep)
	last := a.Messages[len(a.Messages)-1]
	b := last.Blocks[len(last.Blocks)-1]
	if last.Role != llm.RoleUser || !b.Synthetic || !strings.HasPrefix(b.Text, "[harness]") {
		t.Fatalf("note = %+v, want a synthetic [harness] block", b)
	}
	if strings.Contains(b.Text, root) || !strings.Contains(b.Text, "app.go") {
		t.Fatalf("note = %q, want app.go relative to the root", b.Text)
	}
	if !strings.HasSuffix(b.Text, "\n") {
		t.Fatalf("note = %q, want it to end its own paragraph", b.Text)
	}
}
