package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const (
	uEdit = `{"path":"a.go","content":"package m\n\nfunc A() int { return 1 }\n"}`
	uTest = `{"command":"cd ROOT && go test ./..."}`
)

func uncheckedRun(t *testing.T, off map[string]bool, steps ...*llm.Response) (*fake, *Agent) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	for _, s := range steps {
		for i := range s.Message.Blocks {
			b := &s.Message.Blocks[i]
			b.Input = []byte(strings.ReplaceAll(string(b.Input), "ROOT", root))
		}
	}
	f := &fake{responses: steps}
	a := &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, UncheckedNote: true, Off: off,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
	if err := a.Run(context.Background(), "add A", nil); err != nil {
		t.Fatal(err)
	}
	return f, a
}

func sawUncheckedNote(f *fake) int {
	n := 0
	for _, r := range f.requests {
		if strings.Contains(diagLastText(r), uncheckedNote) {
			n++
		}
	}
	return n
}

func TestUncheckedNoteFiresOnceAndTurnContinues(t *testing.T) {
	f, a := uncheckedRun(t, nil,
		toolCall("t1", "write_file", uEdit),
		text("Done."),
		text("Still done."),
	)
	if len(f.requests) != 3 || sawUncheckedNote(f) != 1 {
		t.Fatalf("%d calls, %d notes; want 3 and 1 (fires at most once)", len(f.requests), sawUncheckedNote(f))
	}
	if a.Stats().UncheckedNotes != 1 {
		t.Fatalf("stats = %d", a.Stats().UncheckedNotes)
	}
}

func TestUncheckedNoteSilentWhenChecked(t *testing.T) {
	f, _ := uncheckedRun(t, nil,
		toolCall("t1", "write_file", uEdit),
		toolCall("t2", "bash", uTest),
		text("Done."),
	)
	if len(f.requests) != 3 || sawUncheckedNote(f) != 0 {
		t.Fatalf("%d calls, %d notes; want 3 and 0", len(f.requests), sawUncheckedNote(f))
	}
}

func TestUncheckedNoteFiresOnEditAfterCheck(t *testing.T) {
	f, _ := uncheckedRun(t, nil,
		toolCall("t1", "write_file", uEdit),
		toolCall("t2", "bash", uTest),
		toolCall("t3", "write_file", `{"path":"a.go","content":"package m\n\nfunc A() int { return 2 }\n"}`),
		text("Done."),
		text("Done."),
	)
	if sawUncheckedNote(f) != 1 {
		t.Fatalf("%d notes, want 1", sawUncheckedNote(f))
	}
}

func TestUncheckedNoteSilentWithoutEdits(t *testing.T) {
	f, _ := uncheckedRun(t, nil, text("Nothing to change."))
	if len(f.requests) != 1 || sawUncheckedNote(f) != 0 {
		t.Fatalf("%d calls, %d notes", len(f.requests), sawUncheckedNote(f))
	}
}

func TestUncheckedNoteSwitchOff(t *testing.T) {
	f, _ := uncheckedRun(t, map[string]bool{"unverified": true},
		toolCall("t1", "write_file", uEdit),
		text("Done."),
	)
	if len(f.requests) != 2 || sawUncheckedNote(f) != 0 {
		t.Fatalf("%d calls, %d notes; want 2 and 0", len(f.requests), sawUncheckedNote(f))
	}
}

func TestUncheckedNoteNeedsARunnableCheck(t *testing.T) {
	root := t.TempDir()
	a := &Agent{}
	if a.projectHasCheck(root) {
		t.Fatal("empty dir has a check")
	}
	os.WriteFile(filepath.Join(root, "x_test.py"), nil, 0o644)
	if !a.projectHasCheck(root) {
		t.Fatal("test file not seen")
	}
}
