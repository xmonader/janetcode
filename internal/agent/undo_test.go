package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestUndoRestoresLastTurn(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		// turn 1: create x, edit y (twice, only the first snapshot counts)
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		toolCall("t2", "edit_file", `{"path":"y","old_string":"one","new_string":"two"}`),
		toolCall("t3", "edit_file", `{"path":"y","old_string":"two","new_string":"three"}`),
		text("done"),
		// turn 2: no file changes
		text("nothing"),
		// turn 3: overwrite y
		toolCall("t4", "write_file", `{"path":"y","content":"turn3"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	os.WriteFile(filepath.Join(root, "y"), []byte("one"), 0o644)

	for _, p := range []string{"write", "chat", "write again"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(a.Checkpoints()); got != 2 {
		t.Fatalf("checkpoints = %d, want 2 (turns without edits leave none)", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y")); string(data) != "turn3" {
		t.Fatalf("y = %q", data)
	}

	done, err := a.Undo(context.Background())
	if err != nil || len(done) != 1 {
		t.Fatalf("undo 1: %v %v", done, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y")); string(data) != "three" {
		t.Fatalf("after undo 1, y = %q", data)
	}

	done, err = a.Undo(context.Background())
	if err != nil || len(done) != 2 {
		t.Fatalf("undo 2: %v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("x should be removed by undo")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y")); string(data) != "one" {
		t.Fatalf("after undo 2, y = %q", data)
	}
	if _, err := a.Undo(context.Background()); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("third undo err = %v", err)
	}
}

func TestUndoPersistsAcrossAgents(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.UndoPath = filepath.Join(t.TempDir(), "s.undo.json")
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(a.UndoPath)
	if err != nil {
		t.Fatal("undo file not written after the turn")
	}
	// It is a session sidecar holding the contents of the files the turn
	// changed, so it is as private as the session log beside it.
	if perm := st.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		t.Fatalf("undo file is %v, want it readable by the user alone", perm)
	}
	// A fresh agent (a resumed session) picks the checkpoints up.
	b := &Agent{}
	b.UndoPath = a.UndoPath
	if err := b.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if n := len(b.Checkpoints()); n != 1 {
		t.Fatalf("loaded %d checkpoints", n)
	}
	if _, err := b.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo from the resumed agent did not remove x")
	}
	// The undone state stays on disk so a later resume can redo it.
	c := &Agent{}
	c.UndoPath = a.UndoPath
	if err := c.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Redo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err != nil {
		t.Fatal("redo from a resumed agent did not recreate x")
	}
	if _, err := c.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.cp.dropRedo(context.Background(), nil)
	c.cp.changes = nil // provenance keeps the file otherwise
	if err := c.SaveUndo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.UndoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty checkpoint and redo lists should remove the undo file")
	}
	os.WriteFile(a.UndoPath, []byte("{bad"), 0o644)
	if err := b.LoadUndo(); err == nil {
		t.Fatal("corrupt undo file accepted")
	}
}

// gitInit creates a repo at root with a repo-local identity (see
// gitxtest) and one commit, so both the test's own git commands and any
// git command the agent under test issues on its own (through the bash
// tool, with a scrubbed environment) can commit without a global git
// config.
func gitInit(t *testing.T, root string) {
	t.Helper()
	gitxtest.InitRepo(t, root)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestUndoRevertsShellChangesThroughGit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		// turn 1: a shell command that only reads leaves no checkpoint
		toolCall("t0", "bash", `{"command":"cat a.txt"}`),
		text("read"),
		// turn 2: shell creates, edits and deletes; then a file tool edits
		toolCall("t1", "bash", `{"command":"echo made > made.txt; echo changed > a.txt; rm b.txt"}`),
		toolCall("t2", "write_file", `{"path":"w.txt","content":"w"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o644)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	for _, p := range []string{"look", "change"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || cps[0].Snap == nil || len(cps[0].Files) != 1 {
		t.Fatalf("checkpoints = %+v", cps)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "changed\n" {
		t.Fatalf("a.txt = %q", data)
	}
	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(done, "\n")
	for _, want := range []string{"removed " + filepath.Join(root, "made.txt"), "restored " + filepath.Join(root, "a.txt"), "restored " + filepath.Join(root, "b.txt"), "removed " + filepath.Join(root, "w.txt")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt after undo = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("made.txt still there")
	}
	if _, err := a.Undo(context.Background()); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("second undo err = %v", err)
	}
	refs := func() string {
		cmd := exec.Command("git", "for-each-ref", snapshot.RefPrefix)
		cmd.Dir = root
		out, _ := cmd.Output()
		return string(out)
	}
	if n := strings.Count(refs(), "\n"); n != 1 {
		t.Fatalf("want exactly the redo snapshot ref, got:\n%s", refs())
	}
	// Redo brings the changes back and makes them undoable again.
	done, err = a.Redo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "changed\n" {
		t.Fatalf("a.txt after redo = %q (done: %v)", data, done)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err != nil {
		t.Fatal("made.txt not recreated by redo")
	}
	if _, err := a.Redo(context.Background()); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("second redo err = %v", err)
	}
	preview, err := a.UndoPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(preview, "\n")
	for _, want := range []string{"remove " + filepath.Join(root, "made.txt"), "restore " + filepath.Join(root, "a.txt")} {
		if !strings.Contains(joined, want) {
			t.Errorf("preview missing %q in:\n%s", want, joined)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "changed\n" {
		t.Fatal("preview changed the tree")
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt after undo of redo = %q", data)
	}
	// A new change discards the redo state and its ref.
	f.responses = append(f.responses, toolCall("t9", "write_file", `{"path":"c.txt","content":"new"}`), text("ok"))
	if err := a.Run(context.Background(), "write c", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redo(context.Background()); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("redo after new change err = %v", err)
	}
	if n := strings.Count(refs(), "\n"); n != 1 {
		t.Fatalf("want only the new turn's ref, got:\n%s", refs())
	}
}

func TestUndoFallsBackToFileCopiesWhenHeadMoved(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo shell > s.txt"}`),
		toolCall("t2", "write_file", `{"path":"a.txt","content":"edited"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	a.Snapshots, _ = snapshot.Open(context.Background(), root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root) // the user commits: HEAD moves
	done, err := a.Undo(context.Background())
	if !errors.Is(err, snapshot.ErrHeadMoved) || len(done) != 1 {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "s.txt")); err != nil {
		t.Fatal("shell file should be left alone when HEAD moved")
	}
}

// TestUndoRollsBackTheConversation: undoing a turn takes its messages
// out of the context as well as its file changes, leaving one note in
// their place; a turn that changed nothing leaves the conversation alone.
func TestUndoRollsBackTheConversation(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "write x", nil); err != nil {
		t.Fatal(err)
	}
	before := len(a.Messages)
	if before < 4 {
		t.Fatalf("expected a full turn in the context, have %d messages", before)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !os.IsNotExist(err) {
		t.Fatal("x should be gone")
	}
	// The turn began with the prompt at index 0, so the note is all that
	// remains.
	if len(a.Messages) != 1 || !a.Messages[0].Blocks[0].Synthetic || !strings.Contains(a.Messages[0].Text(), "undid the previous turn") {
		t.Fatalf("messages after undo = %d: %+v", len(a.Messages), a.Messages)
	}
	if a.LastUndoRemoved() != before {
		t.Fatalf("removed = %d, want %d", a.LastUndoRemoved(), before)
	}
}

// TestAutoEditsLetsEditsThroughAndCommandsAsk: with AutoEdits the file
// tools run without the approver and the shell still goes through it.
func TestAutoEditsLetsEditsThroughAndCommandsAsk(t *testing.T) {
	var asked []string
	deny := ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		asked = append(asked, c.Name)
		return Deny, nil
	})
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		toolCall("t2", "bash", `{"command":"echo hi"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, deny)
	a.AutoEdits = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err != nil {
		t.Fatal("write_file should have gone through without asking")
	}
	if len(asked) != 1 || asked[0] != "bash" {
		t.Fatalf("approver asked for %v, want just bash", asked)
	}
}

// AUTO lets in-workspace edits and known-safe reads through, and still
// asks for a mutating command. Guard still wins for a command that
// leaves the workspace.
func TestAutoModeLetsEditsAndSafeReadsThrough(t *testing.T) {
	var asked []string
	deny := ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		asked = append(asked, c.Name)
		return Deny, nil
	})
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		toolCall("t2", "bash", `{"command":"ls"}`),
		toolCall("t3", "bash", `{"command":"echo hi"}`),
		toolCall("t4", "bash", `{"command":"rm x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, deny)
	a.AutoMode = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err != nil {
		t.Fatal("write_file should have gone through without asking")
	}
	if len(asked) != 1 || asked[0] != "bash" {
		t.Fatalf("approver asked for %v, want just the mutating bash", asked)
	}
}

// A file whose state before the turn could not be read is recorded as a
// failed capture: an undo says so and leaves the file alone, rather than
// writing an empty copy over it and reporting a restore.
func TestUndoReportsAFailedCapture(t *testing.T) {
	root := t.TempDir()
	// A directory is not a file the copier can read, and is portable in
	// a way chmod is not.
	path := filepath.Join(root, "blocked")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep.txt"), []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	a.cp.beginTurn(1, 0)
	a.cp.snapshot(context.Background(), nil, "", touchTool{paths: []string{path}}, nil)
	cps := a.Checkpoints()
	if len(cps) != 1 || len(cps[0].Files) != 1 || !cps[0].Files[0].Failed {
		t.Fatalf("checkpoint = %+v", cps)
	}
	preview, err := a.UndoPreview(context.Background())
	if err != nil || len(preview) != 1 || !strings.Contains(preview[0], "cannot restore") {
		t.Fatalf("preview = %v, %v", preview, err)
	}
	done, err := a.Undo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not be captured") {
		t.Fatalf("err = %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("nothing was restored, but undo reported %v", done)
	}
	if _, serr := os.Stat(filepath.Join(path, "keep.txt")); serr != nil {
		t.Fatalf("undo damaged what it could not capture: %v", serr)
	}
}

// touchTool is a mutating tool that declares the paths it would write.
type touchTool struct{ paths []string }

func (touchTool) Name() string                                         { return "touch" }
func (touchTool) Description() string                                  { return "touches files" }
func (touchTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (touchTool) Mutating() bool                                       { return true }
func (touchTool) Run(context.Context, json.RawMessage) (string, error) { return "ok", nil }
func (t touchTool) Touches(json.RawMessage) ([]string, error)          { return t.paths, nil }

// TestUndoLeavesWorkDoneAfterTheTurnAlone is the data-loss fix: an undo
// used to restore the whole diff against the snapshot — deleting any
// file created since the turn (the user's own notes included) and
// overwriting edits the user made after it. The turn's Touched manifest
// now records what the turn itself left in the tree, and the restore
// skips and reports anything whose current state is not that.
func TestUndoLeavesWorkDoneAfterTheTurnAlone(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo made > made.txt; echo changed > a.txt"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "change", nil); err != nil {
		t.Fatal(err)
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || len(cps[0].Touched) == 0 {
		t.Fatalf("checkpoint has no Touched manifest: %+v", cps)
	}
	// After the turn: the user hand-edits a.txt and writes notes.md.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(done, "\n")
	if !strings.Contains(joined, "left ") {
		t.Fatalf("undo said nothing about the foreign changes: %q", joined)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "user edit\n" {
		t.Fatalf("the user's edit was clobbered: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "notes.md")); string(data) != "my notes\n" {
		t.Fatal("the user's notes were removed")
	}
	// The turn's own change is still reverted.
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the turn's own created file should be removed by the undo")
	}
}

// gitAgent is the common setup for the manifest tests: a real repository
// with a real snapshotter behind it, and auto-approved tools.
func gitAgent(t *testing.T, f *fake) (*Agent, string) {
	t.Helper()
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	return a, root
}

// TestUndoWithAnEmptyGitDiffStillScopesTheRestore: a turn whose net effect
// on the tree is nothing — the model rewrote a file with the bytes already
// in it — left an empty manifest, and an empty manifest was
// indistinguishable from no manifest at all, so the undo fell back to
// restoring the whole diff against the snapshot and took the user's later
// work with it.
func TestUndoWithAnEmptyGitDiffStillScopesTheRestore(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"alpha\n"}`),
		text("done"),
	}}
	a, root := gitAgent(t, f)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "rewrite it the same", nil); err != nil {
		t.Fatal(err)
	}
	cps := a.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("checkpoints = %d, want 1", len(cps))
	}
	if !cps[0].Manifested {
		t.Fatal("the turn left no manifest, so its undo is unscoped")
	}

	// After the turn: the user edits the file and writes a new one.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "user edit\n" {
		t.Fatalf("the user's edit was clobbered: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md")); err != nil {
		t.Fatal("the user's notes were removed by an undo that should have touched nothing")
	}
}

// TestUndoOutsideGitLeavesWorkDoneAfterTheTurnAlone: the file-copy
// fallback — every workspace that is not a git repository — consulted no
// manifest at all and wrote the pre-turn bytes back over whatever was
// there, which is the same data loss the git path was fixed for.
func TestUndoOutsideGitLeavesWorkDoneAfterTheTurnAlone(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"from the turn\n"}`),
		toolCall("t2", "write_file", `{"path":"made.txt","content":"also the turn\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if cps := a.Checkpoints(); len(cps) != 1 || !cps[0].Manifested {
		t.Fatalf("no manifest outside git: %+v", cps)
	}
	// After the turn the user edits one of the turn's files by hand.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(done, "\n"), "left "+filepath.Join(root, "a.txt")+" alone") {
		t.Fatalf("undo said nothing about the foreign change: %q", done)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "user edit\n" {
		t.Fatalf("the user's edit was clobbered: %q", data)
	}
	// The turn's untouched file is still reverted.
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the turn's own file should be removed by the undo")
	}
}

// TestRedoThenUndoLeavesLaterWorkAlone: the state captured before a
// restore is itself a checkpoint, and captureState recorded no manifest
// for it — so one /redo put an unscoped checkpoint back on the undo stack
// and the next /undo restored the whole tree again.
func TestRedoThenUndoLeavesLaterWorkAlone(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo changed > a.txt"}`),
		text("done"),
	}}
	a, root := gitAgent(t, f)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "change", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Redo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cps := a.Checkpoints(); len(cps) != 1 || !cps[0].Manifested {
		t.Fatalf("the redo left an unmanifested checkpoint: %+v", cps)
	}
	// After the redo the user writes a file of their own.
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md")); err != nil {
		t.Fatal("the undo after a redo removed the user's file")
	}
	// The redone change is undone again, which is the point of the undo.
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q, want the pre-turn content", data)
	}
}

// brokenDiff is a Snapshotter that can take and restore but cannot say
// what changed — an index.lock, a timeout, a missing object.
type brokenDiff struct{ restored bool }

func (b *brokenDiff) Take(context.Context, string) (snapshot.Snap, error) {
	return snapshot.Snap{Commit: "taken"}, nil
}
func (b *brokenDiff) Restore(context.Context, snapshot.Snap) ([]string, error) {
	b.restored = true
	return []string{"restored a.txt"}, nil
}
func (b *brokenDiff) RestoreSkip(_ context.Context, _ snapshot.Snap, _ map[string]bool) ([]string, error) {
	b.restored = true
	return []string{"restored a.txt"}, nil
}
func (b *brokenDiff) Drop(context.Context, snapshot.Snap)                  {}
func (b *brokenDiff) Changed(context.Context, snapshot.Snap) (bool, error) { return true, nil }
func (b *brokenDiff) Diff(context.Context, snapshot.Snap) ([]string, error) {
	return nil, errors.New("fatal: unable to read index")
}
func (b *brokenDiff) Moved(context.Context, snapshot.Snap) (bool, error) { return false, nil }

// TestUndoRefusesWhenGitCannotSayWhatChanged: foreignChanges used to
// swallow a Diff error and return an empty skip set, which is exactly
// "restore everything" — so any transient git failure silently turned a
// scoped undo back into the destructive one.
func TestUndoRefusesWhenGitCannotSayWhatChanged(t *testing.T) {
	repo := &brokenDiff{}
	snap := snapshot.Snap{Commit: "before"}
	a := &Agent{}
	a.snapshotter = repo
	a.cp.all = []Checkpoint{{
		Turn: 1, Snap: &snap, Manifested: true,
		Touched: []TouchedFile{{Path: "a.txt", Sum: "deadbeef"}},
	}}
	_, err := a.Undo(context.Background())
	if err == nil {
		t.Fatal("undo reported success although git could not say what changed")
	}
	if !strings.Contains(err.Error(), "nothing was reverted") {
		t.Fatalf("err = %v, want a refusal that says nothing was reverted", err)
	}
	if repo.restored {
		t.Fatal("the undo restored the tree anyway")
	}
	// Nothing was reverted, so the turn is still the one to undo: the
	// refusal used to pop it for good, and a retry once git recovered
	// undid the turn before it instead.
	if cps := a.Checkpoints(); len(cps) != 1 || cps[0].Turn != 1 {
		t.Fatalf("checkpoints after the refused undo = %+v, want turn 1 still there", cps)
	}
	if _, err := a.Redo(context.Background()); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("a refused undo left something to redo: %v", err)
	}
}

// TestUndoRevertsATypechange: git reports a file swapped for a symlink as
// T, and the manifest dropped every status it did not know — so the
// turn's own typechange was left in the tree and the undo told the user it
// had been changed after the turn.
func TestUndoRevertsATypechange(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm a.txt && ln -s b.txt a.txt"}`),
		text("done"),
	}}
	a, root := gitAgent(t, f)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "swap it", nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "a.txt")
	if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the turn did not leave a symlink: %v %v", st, err)
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || len(cps[0].Touched) != 1 || cps[0].Touched[0].Sum == "" {
		t.Fatalf("the typechange is not in the manifest: %+v", cps)
	}
	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(done, "\n"), "left ") {
		t.Fatalf("the undo blamed the user for the turn's own typechange: %q", done)
	}
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("a.txt was not put back as a regular file: %v %v", st, err)
	}
	if data, _ := os.ReadFile(p); string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q", data)
	}
}

// recordingSnaps reports one changed path and records which restore the
// undo asked for.
type recordingSnaps struct {
	changed  string
	skip     map[string]bool
	unscoped bool
}

func (r *recordingSnaps) Take(context.Context, string) (snapshot.Snap, error) {
	return snapshot.Snap{Commit: "taken"}, nil
}
func (r *recordingSnaps) Restore(context.Context, snapshot.Snap) ([]string, error) {
	r.unscoped = true
	return nil, nil
}
func (r *recordingSnaps) RestoreSkip(_ context.Context, _ snapshot.Snap, skip map[string]bool) ([]string, error) {
	r.skip = skip
	return nil, nil
}
func (r *recordingSnaps) Drop(context.Context, snapshot.Snap)                  {}
func (r *recordingSnaps) Changed(context.Context, snapshot.Snap) (bool, error) { return true, nil }
func (r *recordingSnaps) Diff(context.Context, snapshot.Snap) ([]string, error) {
	return []string{"M " + r.changed}, nil
}
func (r *recordingSnaps) Moved(context.Context, snapshot.Snap) (bool, error) { return false, nil }

// TestAnEmptyManifestIsNotAMissingManifest: a turn that left the tree as
// it found it records an empty manifest, and its undo must touch nothing.
// A checkpoint from before manifests existed has no manifest, and restores
// everything as it always did. The two used to be the same value.
func TestAnEmptyManifestIsNotAMissingManifest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		manifested bool
		wantSkip   bool
	}{
		{"empty manifest skips the foreign change", true, true},
		{"no manifest restores everything", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingSnaps{changed: filepath.Join(t.TempDir(), "a.txt")}
			snap := snapshot.Snap{Commit: "before"}
			a := &Agent{}
			a.snapshotter = repo
			a.cp.all = []Checkpoint{{Turn: 1, Snap: &snap, Manifested: tc.manifested}}
			if _, err := a.Undo(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.wantSkip {
				if repo.unscoped {
					t.Fatal("an empty manifest restored the whole tree")
				}
				if !repo.skip[repo.changed] {
					t.Fatalf("the foreign change was not skipped: %v", repo.skip)
				}
				return
			}
			if !repo.unscoped {
				t.Fatalf("a checkpoint with no manifest should restore everything, skip = %v", repo.skip)
			}
		})
	}
}

// TestUndoRevertsAnIgnoredFile: git's snapshot does not record an
// ignored file, and the git path of the undo returned without looking at
// the file tools' copies — so a turn's edit to .env stayed, and the undo
// reported nothing wrong.
func TestUndoRevertsAnIgnoredFile(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":".env","content":"TURN=1\n"}`),
		toolCall("t2", "write_file", `{"path":"made.log","content":"new\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n*.log\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=orig\n"), 0o644)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".env")); string(data) != "SECRET=orig\n" {
		t.Fatalf(".env = %q after undo (done: %q)", data, done)
	}
	if _, err := os.Stat(filepath.Join(root, "made.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the ignored file the turn created is still there")
	}
	// And redo brings the turn's version back.
	if _, err := a.Redo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".env")); string(data) != "TURN=1\n" {
		t.Fatalf(".env = %q after redo", data)
	}
}
