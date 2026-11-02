package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

const mermaidMsg = "Here is the flow:\n\n```mermaid\nflowchart TD\n  A[Parse] --> B{Supported?}\n  B -->|yes| C[Draw]\n  B -->|no| D[Source]\n```\n\nThat is the whole of it.\n"

func assistantSaid(tr *transcript, text string) {
	tr.apply(agent.Event{Kind: agent.EvText, Text: text})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
}

// TestMermaidRendersAndKeepsItsSource: a mermaid block is drawn rather
// than dumped, and ctrl+o (which is what toggleLastTurn does) brings the
// source back — the picture never costs the reader the text.
func TestMermaidRendersAndKeepsItsSource(t *testing.T) {
	tr := newTranscript()
	assistantSaid(tr, mermaidMsg)
	o := renderOpts{width: 90, renderer: builtinRenderers}
	out := plain(tr.render(o))
	if !strings.Contains(out, "┌") || !strings.Contains(out, "Supported?") {
		t.Fatalf("the diagram was not drawn:\n%s", out)
	}
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("the source was dumped as well as drawn:\n%s", out)
	}
	if !strings.Contains(out, "ctrl+o") {
		t.Fatalf("a drawn diagram should say where its source went:\n%s", out)
	}
	if !strings.Contains(out, "That is the whole of it.") {
		t.Fatalf("the prose around the diagram was lost:\n%s", out)
	}
	tr.toggleLastTurn()
	back := plain(tr.render(o))
	if !strings.Contains(back, "flowchart TD") || !strings.Contains(back, "-->|yes|") {
		t.Fatalf("expanding should show the mermaid source:\n%s", back)
	}
	// And folding it again draws the diagram, rather than leaving the
	// source on screen from the cached render.
	tr.toggleLastTurn()
	if again := plain(tr.render(o)); strings.Contains(again, "flowchart TD") {
		t.Fatalf("folding should draw the diagram again:\n%s", again)
	}
}

// TestMermaidFallsBack: every condition that cannot produce an honest
// picture leaves the source alone.
func TestMermaidFallsBack(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		ansi  bool
		keep  string
	}{
		{name: "narrow terminal", text: mermaidMsg, width: 30, keep: "flowchart TD"},
		{name: "ansi-only terminal", text: mermaidMsg, width: 90, ansi: true, keep: "flowchart TD"},
		{
			name: "a kind we do not parse", width: 90, keep: "gantt",
			text: "```mermaid\ngantt\n  title A\n```\n",
		},
		{
			name: "a malformed diagram", width: 90, keep: "A[Start --> B",
			text: "```mermaid\nflowchart TD\n  A[Start --> B\n```\n",
		},
		{
			name: "another language's fence", width: 90, keep: "func main()",
			text: "```go\nfunc main() {}\n```\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sty := defaultStyles()
			sty.ansiOnly = tc.ansi
			tr := newTranscriptStyled("dark", sty)
			assistantSaid(tr, tc.text)
			out := plain(tr.render(renderOpts{width: tc.width, renderer: builtinRenderers}))
			if !strings.Contains(out, tc.keep) {
				t.Fatalf("expected the source %q to survive:\n%s", tc.keep, out)
			}
			if strings.Contains(out, "ctrl+o shows the mermaid source") {
				t.Fatalf("nothing was drawn, so nothing should claim to have been:\n%s", out)
			}
		})
	}
}

// TestMermaidWhileStreaming: a half-arrived diagram is not drawn — the
// source so far is, so the transcript does not flicker through a series
// of wrong pictures.
func TestMermaidWhileStreaming(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvText, Text: "```mermaid\nflowchart TD\n  A[Parse] --> B[Draw]\n"})
	out := plain(tr.render(renderOpts{width: 90, renderer: builtinRenderers}))
	if strings.Contains(out, "ctrl+o shows the mermaid source") {
		t.Fatalf("an unfinished diagram should not be drawn:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvText, Text: "```\n"})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	done := plain(tr.render(renderOpts{width: 90, renderer: builtinRenderers}))
	if !strings.Contains(done, "┌") {
		t.Fatalf("the finished diagram should be drawn:\n%s", done)
	}
}

// TestMermaidOffKeepsTheSource: Off["mermaid"] restores the pre-feature
// transcript, where a fenced mermaid block is shown as source, not drawn.
func TestMermaidOffKeepsTheSource(t *testing.T) {
	tr := newTranscript()
	assistantSaid(tr, mermaidMsg)
	out := plain(tr.render(renderOpts{width: 90, renderer: builtinRenderers, noMermaid: true}))
	if !strings.Contains(out, "flowchart TD") {
		t.Fatalf("mermaid off should keep the source:\n%s", out)
	}
	if strings.Contains(out, "┌") || strings.Contains(out, "ctrl+o shows the mermaid source") {
		t.Fatalf("mermaid off must not draw the diagram:\n%s", out)
	}
}

// TestMermaidFitsTheWidth: a diagram never draws past the transcript.
func TestMermaidFitsTheWidth(t *testing.T) {
	for _, w := range []int{46, 60, 100} {
		tr := newTranscript()
		assistantSaid(tr, mermaidMsg)
		for i, l := range strings.Split(plain(tr.render(renderOpts{width: w, renderer: builtinRenderers})), "\n") {
			if n := len([]rune(l)); n > w {
				t.Errorf("width %d: line %d is %d columns: %q", w, i, n, l)
			}
		}
	}
}
