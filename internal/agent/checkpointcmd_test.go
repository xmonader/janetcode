package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func cpRun(t *testing.T, a *Agent, root, arg string) (sheet, errText string, notes []string) {
	t.Helper()
	res, err := a.CheckpointCommand(context.Background(), root, arg)
	if err != nil {
		t.Fatalf("/checkpoints %s: %v", arg, err)
	}
	return res.Sheet, res.Error, res.Notes
}

func TestParseCheckpointArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want cpCommand
		bad  bool
	}{
		{in: "diff 3", want: cpCommand{verb: "diff", span: cpSpan{From: 3}}},
		{in: "diff t3 5", want: cpCommand{verb: "diff", span: cpSpan{From: 3, To: 5}}},
		{in: "diff 3 now", want: cpCommand{verb: "diff", span: cpSpan{From: 3, Now: true}}},
		{in: "export 2 out/x.patch", want: cpCommand{verb: "export", span: cpSpan{From: 2}, path: "out/x.patch"}},
		{in: "export 2 now x.patch", want: cpCommand{verb: "export", span: cpSpan{From: 2, Now: true}, path: "x.patch"}},
		{in: "commit 4 fix the thing, really", want: cpCommand{verb: "commit", span: cpSpan{From: 4}, message: "fix the thing, really"}},
		{in: "commit apply", want: cpCommand{verb: "commit", apply: true}},
		{in: "diff", bad: true},
		{in: "diff x", bad: true},
		{in: "diff 0", bad: true},
		{in: "diff 3 5 extra", bad: true},
		{in: "commit", bad: true},
		{in: "restore 3", bad: true},
	} {
		got, err := parseCheckpointArgs(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q should be a usage error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestCheckpointDiffOwnTurnRangeAndNow(t *testing.T) {
	a, root := threeTurns(t)
	// Turn 2's own work: two.txt created and a.txt rewritten, not turn 3's.
	sheet, errText, _ := cpRun(t, a, root, "diff 2")
	if errText != "" {
		t.Fatal(errText)
	}
	for _, want := range []string{"before turn 2 → before turn 3", "+two", "-alpha", "+rewritten", "new file mode"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("diff 2 lacks %q:\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, "three.txt") || strings.Contains(sheet, "one.txt") {
		t.Errorf("diff 2 must be turn 2 alone:\n%s", sheet)
	}
	// The newest turn runs to the work tree.
	sheet, _, _ = cpRun(t, a, root, "diff 3")
	if !strings.Contains(sheet, "the work tree now") || !strings.Contains(sheet, "+three") {
		t.Errorf("diff 3:\n%s", sheet)
	}
	// A range, and everything since.
	sheet, _, _ = cpRun(t, a, root, "diff 1 3")
	if !strings.Contains(sheet, "one.txt") || !strings.Contains(sheet, "two.txt") || strings.Contains(sheet, "three.txt") {
		t.Errorf("diff 1 3 is turns 1 and 2:\n%s", sheet)
	}
	sheet, _, _ = cpRun(t, a, root, "diff 1 now")
	for _, f := range []string{"one.txt", "two.txt", "three.txt"} {
		if !strings.Contains(sheet, f) {
			t.Errorf("diff 1 now lacks %s:\n%s", f, sheet)
		}
	}
	// A work-tree edit shows against "now" and not against a range.
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one, then edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sheet, _, _ = cpRun(t, a, root, "diff 1 now"); !strings.Contains(sheet, "+one, then edited") {
		t.Errorf("diff now must see the edit:\n%s", sheet)
	}
	if sheet, _, _ = cpRun(t, a, root, "diff 1 2"); strings.Contains(sheet, "then edited") {
		t.Errorf("a range between checkpoints must not see the edit:\n%s", sheet)
	}
	// Refusals say why.
	for arg, want := range map[string]string{
		"diff 9":   "no checkpoint for turn 9",
		"diff 2 2": "not after turn 2",
		"diff 1 7": "no checkpoint for turn 7",
	} {
		if _, e, _ := cpRun(t, a, root, arg); !strings.Contains(e, want) {
			t.Errorf("%s: error %q lacks %q", arg, e, want)
		}
	}
}

func TestCheckpointDiffOfAnUnchangedTurnSaysSo(t *testing.T) {
	a, root := threeTurns(t)
	// Put turn 3's file back by hand: the work tree then equals before turn 3.
	if err := os.Remove(filepath.Join(root, "three.txt")); err != nil {
		t.Fatal(err)
	}
	sheet, _, _ := cpRun(t, a, root, "diff 3")
	if !strings.Contains(sheet, "no difference") {
		t.Errorf("an empty comparison must say so:\n%s", sheet)
	}
	if _, e, _ := cpRun(t, a, root, "export 3"); !strings.Contains(e, "nothing to write") {
		t.Errorf("export of nothing: %q", e)
	}
}

func TestCheckpointDiffWithoutGitUsesTheFileCopies(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"changed\n"}`),
		toolCall("t2", "write_file", `{"path":"made.txt","content":"fresh\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	sheet, e, _ := cpRun(t, a, root, "diff 1")
	if e != "" {
		t.Fatal(e)
	}
	for _, want := range []string{"-alpha", "+changed", "+fresh", "new file mode", "file copies only"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("copies diff lacks %q:\n%s", want, sheet)
		}
	}
}

// The copies-only patch is a real patch: git apply takes it on the
// tree as it was before the turn, for a modified, a created and a
// deleted file alike.
func TestCopiesPatchApplies(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.txt")
	edited := filepath.Join(root, "edited.txt")
	made := filepath.Join(root, "made.txt")
	if err := os.WriteFile(edited, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(made, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cps := []Checkpoint{{Turn: 1, Files: []Snapshot{
		{Path: gone, Existed: true, Data: []byte("bye\n")},
		{Path: edited, Existed: true, Data: []byte("one\nTWO\nthree\n")},
		{Path: made, Existed: false},
	}}, {Turn: 2, Files: []Snapshot{{Path: edited, Existed: true, Data: []byte("ignored: the earlier copy wins\n")}}}}
	patch, note := copiesPatch(root, cps)
	if !strings.Contains(note, "file copies only") {
		t.Fatalf("note = %q", note)
	}
	// The state before the turns, as a clean tree.
	clean := t.TempDir()
	for name, body := range map[string]string{"gone.txt": "bye\n", "edited.txt": "one\nTWO\nthree\n"} {
		if err := os.WriteFile(filepath.Join(clean, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	patchFile := filepath.Join(t.TempDir(), "p.patch")
	if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "apply", patchFile)
	cmd.Dir = clean
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\n--- patch:\n%s", err, out, patch)
	}
	if b, _ := os.ReadFile(filepath.Join(clean, "edited.txt")); string(b) != "one\ntwo\nthree\n" {
		t.Errorf("edited.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(clean, "made.txt")); string(b) != "new\n" {
		t.Errorf("made.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(clean, "gone.txt")); err == nil {
		t.Errorf("gone.txt survived the patch")
	}
}

func TestCheckpointExportWritesAPatchAndRefusesToOverwrite(t *testing.T) {
	a, root := threeTurns(t)
	_, e, notes := cpRun(t, a, root, "export 2")
	if e != "" || len(notes) != 1 {
		t.Fatalf("export: %q %v", e, notes)
	}
	path := filepath.Join(root, "janetcode-checkpoint-t2.patch")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "+rewritten") || strings.Contains(string(data), "three.txt") {
		t.Fatalf("exported patch:\n%s", data)
	}
	// A name that is taken is refused, and the file is untouched.
	_, e, _ = cpRun(t, a, root, "export 2")
	if !strings.Contains(e, "already exists") {
		t.Fatalf("second export: %q", e)
	}
	// It is a real patch: it applies to the tree as it was before turn 2.
	if _, err := a.PreviewUndoTurn(context.Background(), 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// (The restore removed the untracked patch with the rest, so check a copy.)
	copyPath := filepath.Join(t.TempDir(), "t2.patch")
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "apply", "--check", copyPath)
	// An explicit path, in a directory that does not exist yet.
	if _, e, _ = cpRun(t, a, root, "export 1 out/sub/one.patch"); e != "" {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "sub", "one.patch")); err != nil {
		t.Fatal(err)
	}
}

// The commit is opt-in twice over: the preview creates nothing and shows
// the attribution first; only apply commits, exactly the turn's files.
func TestCheckpointCommitPreviewsAttributionThenCommitsOnlyTheTurn(t *testing.T) {
	a, root := threeTurns(t)
	head := gitOut(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	sheet, e, _ := cpRun(t, a, root, "commit 2")
	if e != "" {
		t.Fatal(e)
	}
	for _, want := range []string{"two.txt", "a.txt", "attribution", "your own git identity", "no Co-Authored-By", "nothing has been created yet", "checkpoint t2"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("preview lacks %q:\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, "  three.txt") || strings.Contains(sheet, "  unrelated.txt") {
		t.Errorf("the preview lists files outside turn 2:\n%s", sheet)
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("a preview must not commit")
	}
	if _, e, _ := cpRun(t, a, root, "commit apply"); e != "" {
		t.Fatal(e)
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got == head {
		t.Fatal("apply did not commit")
	}
	files := strings.Fields(gitOut(t, root, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "a.txt,two.txt" {
		t.Fatalf("commit holds %v, want exactly turn 2's files", files)
	}
	msg := gitOut(t, root, "log", "-1", "--format=%B")
	if strings.Contains(strings.ToLower(msg), "co-authored-by") || strings.Contains(strings.ToLower(msg), "generated") {
		t.Fatalf("an attribution trailer was added: %q", msg)
	}
	if !strings.HasPrefix(msg, "checkpoint t2") {
		t.Fatalf("message = %q", msg)
	}
	// The user's own file and turn 3's work stay out of it.
	if st := gitOut(t, root, "status", "--porcelain"); !strings.Contains(st, "unrelated.txt") || !strings.Contains(st, "three.txt") {
		t.Fatalf("other files must stay uncommitted:\n%s", st)
	}
	// Nothing left to apply.
	if _, e, _ := cpRun(t, a, root, "commit apply"); !strings.Contains(e, "no previewed") {
		t.Fatalf("a second apply: %q", e)
	}
}

func TestCheckpointCommitApplyRefusesWhenTheFilesMoved(t *testing.T) {
	a, root := threeTurns(t)
	head := gitOut(t, root, "rev-parse", "HEAD")
	if _, e, _ := cpRun(t, a, root, "commit 3 my own message"); e != "" {
		t.Fatal(e)
	}
	// Someone commits turn 3's file in the meantime: the plan changes.
	gitOut(t, root, "add", "three.txt")
	gitOut(t, root, "commit", "-q", "-m", "by hand")
	_, e, _ := cpRun(t, a, root, "commit apply")
	if e == "" || gitOut(t, root, "log", "--format=%s", head+"..HEAD") != "by hand" {
		t.Fatalf("apply after the files moved: %q", e)
	}
}

func TestCheckpointCommitWithNothingToCommitOrNoTurn(t *testing.T) {
	a, root := threeTurns(t)
	if _, e, _ := cpRun(t, a, root, "commit 9"); !strings.Contains(e, "no checkpoint for turn 9") {
		t.Fatalf("unknown turn: %q", e)
	}
	gitOut(t, root, "add", "one.txt")
	gitOut(t, root, "commit", "-q", "-m", "by hand")
	if _, e, _ := cpRun(t, a, root, "commit 1"); !strings.Contains(e, "nothing to commit") {
		t.Fatalf("already committed: %q", e)
	}
}
