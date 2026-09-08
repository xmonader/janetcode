package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
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
	if _, err := os.Stat(a.UndoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty checkpoint list should remove the undo file")
	}
	os.WriteFile(a.UndoPath, []byte("{bad"), 0o644)
	if err := b.LoadUndo(); err == nil {
		t.Fatal("corrupt undo file accepted")
	}
}
