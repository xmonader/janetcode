package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	done, err := a.Undo()
	if err != nil || len(done) != 1 {
		t.Fatalf("undo 1: %v %v", done, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y")); string(data) != "three" {
		t.Fatalf("after undo 1, y = %q", data)
	}

	done, err = a.Undo()
	if err != nil || len(done) != 2 {
		t.Fatalf("undo 2: %v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("x should be removed by undo")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y")); string(data) != "one" {
		t.Fatalf("after undo 2, y = %q", data)
	}
	if _, err := a.Undo(); !errors.Is(err, ErrNothingToUndo) {
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
	if _, err := os.Stat(a.UndoPath); err != nil {
		t.Fatal("undo file not written after the turn")
	}
	// A fresh agent (a resumed session) picks the checkpoints up.
	b := &Agent{UndoPath: a.UndoPath}
	if err := b.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if n := len(b.Checkpoints()); n != 1 {
		t.Fatalf("loaded %d checkpoints", n)
	}
	if _, err := b.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo from the resumed agent did not remove x")
	}
	// The undone state stays on disk so a later resume can redo it.
	c := &Agent{UndoPath: a.UndoPath}
	if err := c.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Redo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err != nil {
		t.Fatal("redo from a resumed agent did not recreate x")
	}
	if _, err := c.Undo(); err != nil {
		t.Fatal(err)
	}
	c.cp.dropRedo(nil)
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

func gitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
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
	repo, err := snapshot.Open(root)
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
	done, err := a.Undo()
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
	if _, err := a.Undo(); !errors.Is(err, ErrNothingToUndo) {
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
	done, err = a.Redo()
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "changed\n" {
		t.Fatalf("a.txt after redo = %q (done: %v)", data, done)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err != nil {
		t.Fatal("made.txt not recreated by redo")
	}
	if _, err := a.Redo(); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("second redo err = %v", err)
	}
	preview, err := a.UndoPreview()
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
	if _, err := a.Undo(); err != nil {
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
	if _, err := a.Redo(); !errors.Is(err, ErrNothingToRedo) {
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
	a.Snapshots, _ = snapshot.Open(root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root) // the user commits: HEAD moves
	done, err := a.Undo()
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
