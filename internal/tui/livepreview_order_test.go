package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestLivePreviewWaitsForAnEarlierEditOfTheSameFile: the diff shown while
// a reply is still streaming is worked out against the file on disk. Two
// edits of one file in one reply: the second's line numbers are written
// against the file the first leaves, but the first has not run yet, so
// the second's preview was a diff against the old content — here it
// showed line 3 ("3") being replaced when the edit will replace what is
// line 3 after the first edit ("2"). The second waits, saying so, until
// the first has run, and is then previewed against what it will change.
// An edit of another file is previewed at once.
func TestLivePreviewWaitsForAnEarlierEditOfTheSameFile(t *testing.T) {
	t.Run("paths as the tool resolves them", func(t *testing.T) { livePreviewOrder(t, true) })
	t.Run("paths as written", func(t *testing.T) { livePreviewOrder(t, false) })
}

func livePreviewOrder(t *testing.T, viaTool bool) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("1\n2\n3\n4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "g.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := tool.EditFile{Root: root}
	o := renderOpts{width: 80, preview: func(name string, input []byte) string {
		out, err := ed.Preview(context.Background(), input)
		if err != nil {
			return ""
		}
		return out
	}}
	if viaTool {
		// The second edit names the file another way; the tool resolves
		// both to the same file.
		o.touches = func(name string, input []byte) []string {
			p, _ := ed.Touches(input)
			return p
		}
	}
	tr := newTranscript()
	tr.user("edit f twice and g once")
	stream := func(id, input string) {
		c := agent.ToolCall{ID: id, Name: "edit_file"}
		tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c})
		tr.apply(agent.Event{Kind: agent.EvToolInput, Call: &c, Text: input})
	}
	first := `{"path":"f.txt","from_line":1,"to_line":1,"new_string":"0\n1"}`
	stream("e1", first)
	second := "f.txt"
	if viaTool {
		second = path // absolute: the same file as the first's f.txt
	}
	stream("e2", `{"path":"`+second+`","from_line":3,"to_line":3,"new_string":"THREE"}`)
	stream("e3", `{"path":"g.txt","old_string":"x","new_string":"y"}`)
	tr.render(o)

	e1 := plain(tr.renderTool(tr.findTool("e1"), 80, o))
	if !strings.Contains(e1, "+0") {
		t.Fatalf("the first edit should be previewed while the reply streams:\n%s", e1)
	}
	e2 := plain(tr.findTool("e2").rendered)
	if strings.Contains(e2, "THREE") || strings.Contains(e2, "-3") {
		t.Fatalf("the second edit was previewed against the file before the first edit:\n%s", e2)
	}
	if !strings.Contains(e2, "once the earlier edit") {
		t.Fatalf("the second edit should say its preview waits for the earlier edit:\n%s", e2)
	}
	if e3 := plain(tr.findTool("e3").rendered); !strings.Contains(e3, "+y") {
		t.Fatalf("an edit of another file should be previewed at once:\n%s", e3)
	}

	// The first edit runs.
	c1 := agent.ToolCall{ID: "e1", Name: "edit_file", Input: []byte(first)}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c1})
	if _, err := ed.Run(context.Background(), []byte(first)); err != nil {
		t.Fatal(err)
	}
	tr.render(o)
	if e2 := plain(tr.findTool("e2").rendered); strings.Contains(e2, "THREE") {
		t.Fatalf("previewed while the earlier edit was still running:\n%s", e2)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c1, Output: "ok"})
	tr.render(o)
	e2 = plain(tr.findTool("e2").rendered)
	if !strings.Contains(e2, "-2") || !strings.Contains(e2, "+THREE") || strings.Contains(e2, "once the earlier edit") {
		t.Fatalf("once the earlier edit has run the second should be previewed against its result:\n%s", e2)
	}
}
