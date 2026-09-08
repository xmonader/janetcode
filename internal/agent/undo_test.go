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
