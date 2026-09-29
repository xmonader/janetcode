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
)

// dryAgent is an agent whose file and shell tools share one workspace,
// the way the real session's are wired, so a dry run's overlay reaches
// them.
func dryAgent(t *testing.T, responses ...*llm.Response) (*Agent, string) {
	t.Helper()
	f := &fake{responses: responses}
	a, root := newAgent(t, f, nil)
	ws := tool.NewLocal(root)
	a.Tools = tool.NewRegistry(tool.ReadFile{WS: ws}, tool.WriteFile{WS: ws}, tool.EditFile{WS: ws}, tool.Bash{WS: ws})
	a.AutoApprove = true
	return a, root
}

// TestDryRunChangesNothingAndPreviewsEverything is the whole contract of
// a dry-run turn: the file tools write to a preview the model can read
// back and edit again, a command that changes things is recorded and not
// run, a command that only reads runs, and the disk is exactly as it was.
func TestDryRunChangesNothingAndPreviewsEverything(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "write_file", `{"path":"a.txt","content":"new\n"}`),
		toolCall("t2", "edit_file", `{"path":"a.txt","old_string":"new","new_string":"newer"}`),
		toolCall("t3", "read_file", `{"path":"a.txt"}`),
		toolCall("t4", "bash", `{"command":"touch made-by-bash"}`),
		toolCall("t5", "bash", `{"command":"ls"}`),
		toolCall("t6", "bash", `{"command":"ls","background":true}`),
		text("done"),
	)
	a.SetDryRun(true)
	evs, emit := collect()
	if err := a.Run(context.Background(), "change a.txt", emit); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a dry run changed a.txt on disk: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "made-by-bash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a dry run ran a mutating command")
	}
	results := map[string]string{}
	for _, e := range *evs {
		if e.Kind == EvToolResult {
			results[e.Call.ID] = e.Output
		}
	}
	if !strings.Contains(results["t3"], "newer") {
		t.Fatalf("read_file did not see the preview: %q", results["t3"])
	}
	if !strings.Contains(results["t4"], "[dry run]") || !strings.Contains(results["t6"], "[dry run]") {
		t.Fatalf("recorded commands were not reported as not run: %q / %q", results["t4"], results["t6"])
	}
	if !strings.Contains(results["t5"], "a.txt") {
		t.Fatalf("a read-only command should run in a dry run: %q", results["t5"])
	}
	// It read the disk, not the preview, and the model is told so rather
	// than left to conclude its edits never happened.
	if !strings.Contains(results["t5"], "read the disk, not the preview — 1 file has pending changes") {
		t.Fatalf("a read-only command in a dry run did not say it missed the preview: %q", results["t5"])
	}
	report := a.DryRunReport(true)
	for _, want := range []string{"a.txt", "+1 -1", "touch made-by-bash", "-alpha", "+newer", "commands not run (2)"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if len(a.Checkpoints()) != 0 {
		t.Fatalf("a dry run left a checkpoint: %+v", a.Checkpoints())
	}
	if a.LastTurnMutated() {
		t.Fatal("a dry-run turn counted as mutating, which runs the verify gates against an untouched tree")
	}
	// A simulated `python3 test` is no check: the ordinary fold counted
	// the recorded command as one that passed.
	if o := a.Outcome(); o.Verdict != VerdictDryRun || !strings.Contains(o.Why, "1 file(s) previewed, 2 call(s) recorded") {
		t.Fatalf("outcome = %+v", o)
	}
	if !strings.Contains(a.modeSystem(), "# Dry run") {
		t.Fatal("the model was not told it is in a dry run")
	}

	// Applying writes the preview as one undoable checkpoint, ends the
	// dry run, and tells the model what never ran.
	res, err := a.ApplyDryRun(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "newer\n" {
		t.Fatalf("apply wrote %q", data)
	}
	if a.DryRunOn() {
		t.Fatal("apply should end the dry run")
	}
	if !strings.Contains(res.Sheet, "touch made-by-bash") {
		t.Fatalf("apply did not list the commands that never ran: %q", res.Sheet)
	}
	last := a.Messages[len(a.Messages)-1].Text()
	if !strings.Contains(last, "None of the commands") || !strings.Contains(last, "touch made-by-bash") {
		t.Fatalf("the model was not told what never ran: %q", last)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("undo after apply left %q", data)
	}
}

// TestDryRunApplySelected writes only the files picked by number.
func TestDryRunApplySelected(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "write_file", `{"path":"one.txt","content":"1\n"}`),
		toolCall("t2", "write_file", `{"path":"two.txt","content":"2\n"}`),
		text("done"),
	)
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "two files", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DryRunCommand(context.Background(), "apply 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "one.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file that was not picked was written")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "two.txt")); string(data) != "2\n" {
		t.Fatalf("the picked file was not written: %q", data)
	}
}

// TestDryRunScriptsAndSubagentsCannotBypass: a call a script makes goes
// through the same intercept, and an editing subagent is recorded rather
// than started.
func TestDryRunScriptsAndSubagentsCannotBypass(t *testing.T) {
	a, root := dryAgent(t)
	a.SetDryRun(true)
	out, isErr, _ := a.runNested(context.Background(), "s1", "bash", []byte(`{"command":"touch from-script"}`))
	if isErr || !strings.Contains(out, "[dry run]") {
		t.Fatalf("nested call = %q, %v", out, isErr)
	}
	if _, err := os.Stat(filepath.Join(root, "from-script")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a script's call ran in a dry run")
	}
	if !dryMutating(&Task{}, []byte(`{"prompt":"x","edit":true}`)) {
		t.Fatal("an editing subagent is not treated as a change")
	}
	if dryMutating(&Task{}, []byte(`{"prompt":"x"}`)) {
		t.Fatal("a read-only subagent should run in a dry run")
	}
	fork := &Agent{UndoOwner: a}
	if !fork.DryRunOn() {
		t.Fatal("a fork does not share its parent's dry run")
	}
}

// TestDryRunOffDiscardsAndSaysSo: leaving a dry run without applying it
// drops the preview and tells the model none of it happened.
func TestDryRunOffDiscardsAndSaysSo(t *testing.T) {
	a, root := dryAgent(t,
		toolCall("t1", "write_file", `{"path":"a.txt","content":"new\n"}`),
		text("done"),
	)
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.DryRunCommand(context.Background(), "off")
	if err != nil || !strings.Contains(strings.Join(res.Notes, " "), "discarded") {
		t.Fatalf("off = %+v, %v", res, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("discarding wrote the preview: %q", data)
	}
	// The workspace is back on the disk.
	ws := a.workspaces()[0]
	if ws.Overlay() != nil {
		t.Fatal("the overlay outlived the dry run")
	}
	if !strings.Contains(a.Messages[len(a.Messages)-1].Text(), "ended without applying") {
		t.Fatal("the model was not told the preview was dropped")
	}
}
