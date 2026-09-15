package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestFormatChangeGroupsHeadsEachTurnOnce: /changes printed "turn 3 ·
// <intent>" above every row when each change had its own intent, so the
// turn number repeated down the list. Each turn is now one header, its
// intents under it.
func TestFormatChangeGroupsHeadsEachTurnOnce(t *testing.T) {
	root := t.TempDir()
	abs := func(p string) string { return filepath.Join(root, p) }
	groups := []ChangeGroup{
		{Turn: 3, Intent: "add logging", Changes: []Change{{Seq: 1, Turn: 3, Tool: "edit_file", Path: abs("log.go"), Before: []byte("a\n"), After: []byte("a\nb\n"), Existed: true}}},
		{Turn: 3, Intent: "wire it up", Changes: []Change{{Seq: 2, Turn: 3, Tool: "edit_file", Path: abs("main.go"), Before: []byte("x\n"), After: []byte("y\n"), Existed: true, Undone: true}}},
		{Turn: 4, Intent: "fix the test", Changes: []Change{{Seq: 3, Turn: 4, Tool: "write_file", Path: abs("t_test.go"), After: []byte("z\n")}}},
	}
	got := FormatChangeGroups(groups, root, nil)
	if n := strings.Count(got, "turn 3"); n != 1 {
		t.Fatalf("turn 3 appears %d times:\n%s", n, got)
	}
	for _, want := range []string{"turn 3\n", "  add logging\n", "    #1   edit_file  log.go  +1 -0", "  wire it up\n", "main.go  +1 -1  (undone)", "turn 4 · fix the test\n", "  #3   write_file t_test.go  +1 -0"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, root) {
		t.Errorf("absolute paths in:\n%s", got)
	}
}

// TestUndoChangeReportsRelativePath: an undo's report (and the note the
// model gets) named the file by its absolute path.
func TestUndoChangeReportsRelativePath(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"a.txt","content":"one\n"}`),
		toolCall("c2", "write_file", `{"path":"a.txt","content":"two\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.Root = root
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.UndoPath = filepath.Join(t.TempDir(), "u.json")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	done, err := a.UndoChange(2)
	if err != nil {
		t.Fatal(err)
	}
	if done != "reverted 1 hunk(s) of a.txt" {
		t.Fatalf("done = %q", done)
	}
}
