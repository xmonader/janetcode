package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// lateFork is a fork of a that writes after a's turn has ended.
func lateFork(a *Agent, root string) *Agent {
	return &Agent{
		UndoOwner: a, Provider: a.Provider, Model: "m", AutoApprove: true, Root: root,
		Tools: a.Tools,
	}
}

func runLate(t *testing.T, fk *Agent, id, name, input string) string {
	t.Helper()
	out, _, isErr, err := fk.executeTool(context.Background(), llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}, func(Event) {})
	if err != nil || isErr {
		t.Fatalf("%s: %q isErr=%v err=%v", name, out, isErr, err)
	}
	return out
}

// A fork that writes after the turn that started it has ended joins that
// turn's checkpoint, and its files, file-tool and shell alike, are in the
// manifest, so a scoped undo of the turn removes them and still leaves a
// file the user wrote afterwards alone.
func TestUndoTurnIncludesLateForkWrites(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"turn one\n"}`),
		text("done"),
	}}
	a, root := gitAgent(t, f)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Checkpoints()); n != 1 {
		t.Fatalf("checkpoints = %d, want 1", n)
	}
	fk := lateFork(a, root)
	runLate(t, fk, "l1", "write_file", `{"path":"late.txt","content":"from the fork\n"}`)
	runLate(t, fk, "l2", "bash", `{"command":"echo shell > late-shell.txt"}`)
	os.WriteFile(filepath.Join(root, "mine.txt"), []byte("user\n"), 0o644)

	cps := a.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("a late write made %d checkpoints, want it to join the one", len(cps))
	}
	got := map[string]bool{}
	for _, tf := range cps[0].Touched {
		got[filepath.Base(tf.Path)] = true
	}
	for _, want := range []string{"a.txt", "late.txt", "late-shell.txt"} {
		if !got[want] {
			t.Errorf("manifest lacks %s: %+v", want, cps[0].Touched)
		}
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"late.txt", "late-shell.txt"} {
		if _, err := os.Stat(filepath.Join(root, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("undo left the fork's late file %s", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "a\n" {
		t.Errorf("a.txt = %q, want it restored", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "mine.txt")); string(b) != "user\n" {
		t.Errorf("the user's file was touched: %q", b)
	}
}

// When the turn itself changed nothing it left no checkpoint; a fork's
// late write opens one of its own, which /checkpoints lists and undo
// restores.
func TestLateForkWriteOpensItsOwnCheckpoint(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("nothing to do")}}
	a, root := gitAgent(t, f)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Checkpoints()); n != 0 {
		t.Fatalf("checkpoints = %d before the fork wrote", n)
	}
	fk := lateFork(a, root)
	runLate(t, fk, "l1", "write_file", `{"path":"late.txt","content":"x\n"}`)
	os.WriteFile(filepath.Join(root, "mine.txt"), []byte("user\n"), 0o644)
	cps := a.Checkpoints()
	if len(cps) != 1 || !cps[0].Manifested || !strings.HasSuffix(cps[0].Touched[0].Path, "late.txt") {
		t.Fatalf("late write checkpoint = %+v", cps)
	}
	if list := a.CheckpointList(); len(list) != 1 {
		t.Fatalf("/checkpoints lists %d, want 1", len(list))
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "late.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("undo left the late file")
	}
	if _, err := os.Stat(filepath.Join(root, "mine.txt")); err != nil {
		t.Error("undo removed the user's file")
	}
}
