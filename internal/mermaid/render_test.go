package mermaid

import (
	"fmt"
	"strings"
	"testing"
)

const smallFlow = "flowchart TD\n  A[Parse] --> B{ok?}\n  B -->|yes| C[Draw]\n  B -->|no| D[Source]\n"

// A terminal too narrow for boxes gets the source instead.
func TestNarrowFallsBack(t *testing.T) {
	for _, w := range []int{0, 10, 39} {
		if _, ok := Render(smallFlow, w); ok {
			t.Errorf("width %d should have fallen back", w)
		}
	}
	if _, ok := Render(smallFlow, 40); !ok {
		t.Errorf("width 40 should draw")
	}
	// Wide enough for the terminal but not for this diagram: a long
	// label pushes the boxes past the width and the source wins.
	long := "flowchart TD\n  A[" + strings.Repeat("wide ", 12) + "] --> B[b]\n"
	if _, ok := Render(long, 40); ok {
		t.Errorf("a diagram wider than the terminal should have fallen back")
	}
}

// Past the caps the source is easier to read than the picture, and the
// layout is never asked to do the work.
func TestSizeCaps(t *testing.T) {
	var hub strings.Builder
	hub.WriteString("flowchart TD\n")
	for i := 0; i < maxNodes+5; i++ {
		fmt.Fprintf(&hub, "  N%d --> H\n", i)
	}
	if _, err := Parse(hub.String()); err != errTooBig {
		t.Errorf("a %d-node graph: err = %v, want too big", maxNodes+5, err)
	}
	if _, ok := Render(hub.String(), 200); ok {
		t.Errorf("a graph past the node cap should have fallen back")
	}
	var chain strings.Builder
	chain.WriteString("flowchart TD\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&chain, "  N%d --> N%d\n", i, i+1)
	}
	if _, ok := Render(chain.String(), 200); ok {
		t.Errorf("a diagram taller than %d lines should have fallen back", maxLines)
	}
	if _, err := Parse(strings.Repeat("flowchart TD\n  A --> B\n", 2000)); err != errTooBig {
		t.Errorf("a source past %d bytes should be refused", maxSource)
	}
}

// The layout is a pure function of the graph and the width: nothing in
// it may depend on map order or on what was rendered before.
func TestDeterministic(t *testing.T) {
	src := "flowchart TD\n  A --> B\n  A --> C\n  B --> D\n  C --> D\n  D --> E\n  A --> E\n"
	first, ok := Render(src, 80)
	if !ok {
		t.Fatal("expected a diagram")
	}
	for i := 0; i < 20; i++ {
		again, ok := Render(src, 80)
		if !ok || strings.Join(again, "\n") != strings.Join(first, "\n") {
			t.Fatalf("run %d drew something else:\n%s", i, strings.Join(again, "\n"))
		}
	}
}

// A diagram never draws past the width it was given, whatever the
// labels do.
func TestNeverWiderThanGiven(t *testing.T) {
	srcs := []string{
		smallFlow,
		"graph LR\n  A[one] -->|a long edge label here| B[two]\n  B --> C[three]\n",
		"sequenceDiagram\n  A->>B: a message long enough to need the room\n  B-->>A: ok\n",
	}
	for _, w := range []int{40, 55, 80, 120} {
		for _, src := range srcs {
			lines, ok := Render(src, w)
			if !ok {
				continue
			}
			for i, l := range lines {
				if n := runeLen(l); n > w {
					t.Errorf("width %d: line %d is %d columns:\n%s", w, i, n, strings.Join(lines, "\n"))
				}
			}
		}
	}
}

func BenchmarkRenderSmall(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, ok := Render(smallFlow, 80); !ok {
			b.Fatal("fallback")
		}
	}
}

func BenchmarkRenderLarge(b *testing.B) {
	// The largest diagram that still draws: a wide graph at the node cap.
	var sb strings.Builder
	sb.WriteString("flowchart TD\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&sb, "  A --> N%d\n  N%d --> Z\n", i, i)
	}
	src := sb.String()
	if _, ok := Render(src, 200); !ok {
		b.Fatal("fallback")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Render(src, 200)
	}
}
