package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const wrapMarker = "Finish now: give your answer."

// wrapRun plays a turn that writes a small package and goes green, then
// makes the calls of after (a bash command, or "edit" for a source edit),
// and returns how many tool results carried the wrap-up note.
func wrapRun(t *testing.T, headless bool, off map[string]bool, after ...string) (notes int, last string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	goTest := `{"command":"cd ` + root + ` && go test ./..."}`
	rs := []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.go","content":"package m\n\nfunc A() int { return 1 }\n"}`),
		toolCall("t2", "write_file", `{"path":"a_test.go","content":"package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal() } }\n"}`),
		toolCall("t3", "bash", goTest),
	}
	for i, c := range after {
		id := fmt.Sprintf("x%d", i)
		switch c {
		case "edit":
			rs = append(rs, toolCall(id, "write_file", fmt.Sprintf(`{"path":"b.go","content":"package m\n\nfunc B%d() int { return 1 }\n"}`, i)))
		case "green":
			rs = append(rs, toolCall(id, "bash", goTest))
		case "red":
			rs = append(rs, toolCall(id, "bash", `{"command":"cd `+root+` && go test ./nonexistent/..."}`))
		}
	}
	rs = append(rs, text("Done."))
	f := &fake{responses: rs}
	a := &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, WrapUp: headless, Off: off,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
	if err := a.Run(context.Background(), "add A", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && strings.Contains(b.Text, wrapMarker) {
				notes++
				last = b.Text
			}
		}
	}
	return notes, last
}

func greens(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "green"
	}
	return out
}

// The note goes in on the eighth call after the turn first passed, and
// not on the seventh.
func TestWrapUpFiresAfterEightPostGreenCalls(t *testing.T) {
	if n, _ := wrapRun(t, true, nil, greens(wrapUpAfter-1)...); n != 0 {
		t.Fatalf("%d notes after %d post-green calls, want 0", n, wrapUpAfter-1)
	}
	n, note := wrapRun(t, true, nil, greens(wrapUpAfter)...)
	if n != 1 {
		t.Fatalf("%d notes after %d post-green calls, want 1", n, wrapUpAfter)
	}
	if !strings.Contains(note, "go test ./...") || !strings.Contains(note, "8 calls") {
		t.Fatalf("note = %q", note)
	}
}

// Once per turn, however many calls follow.
func TestWrapUpOncePerTurn(t *testing.T) {
	if n, _ := wrapRun(t, true, nil, greens(wrapUpAfter+5)...); n != 1 {
		t.Fatalf("%d notes, want 1", n)
	}
}

// An edit after the last check, or a red last check, is not green: no
// note on that step, and it comes when the state is green again.
func TestWrapUpNeedsAGreenState(t *testing.T) {
	edited := append(greens(wrapUpAfter-1), "edit")
	if n, _ := wrapRun(t, true, nil, edited...); n != 0 {
		t.Fatalf("%d notes with an edit after the last check, want 0", n)
	}
	red := append(greens(wrapUpAfter-1), "red")
	if n, _ := wrapRun(t, true, nil, red...); n != 0 {
		t.Fatalf("%d notes with a red last check, want 0", n)
	}
	if n, _ := wrapRun(t, true, nil, append(edited, "green")...); n != 1 {
		t.Fatalf("%d notes once the state is green again, want 1", n)
	}
}

func TestWrapUpOffAndInteractive(t *testing.T) {
	if n, _ := wrapRun(t, true, map[string]bool{"wrapup": true}, greens(wrapUpAfter+2)...); n != 0 {
		t.Fatalf("%d notes with wrapup off, want 0", n)
	}
	if n, _ := wrapRun(t, false, nil, greens(wrapUpAfter+2)...); n != 0 {
		t.Fatalf("%d notes in interactive mode, want 0", n)
	}
}

// A step that carries the spec review's own wrap-up note is not stacked
// on: the review's steps are neither counted nor given the note.
func TestWrapUpStaysOutOfTheReview(t *testing.T) {
	a := &Agent{WrapUp: true}
	a.reviewSteps = 1
	a.wrap.seen = true
	a.wrap.calls = 20
	if n := a.wrapUpNote(1); n != "" {
		t.Fatalf("note during a review: %q", n)
	}
	if a.wrap.calls != 20 {
		t.Fatalf("review call counted: %d", a.wrap.calls)
	}
}
