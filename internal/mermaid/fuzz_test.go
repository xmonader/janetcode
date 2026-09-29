package mermaid

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Render takes whatever the model wrote in a mermaid fence, so no source
// may panic it, and whatever it draws must fit the width it was given.
func FuzzRender(f *testing.F) {
	for _, s := range []string{
		"flowchart TD\nA[Start] --> B{Ok?}\nB -->|yes| C[Done]\nB -- no --> A",
		"graph LR\nA --> B --> C\nA -.-> C\nB ==> D",
		"sequenceDiagram\nparticipant A as Alice\nA->>B: hi\nB-->>A: yo",
		"stateDiagram-v2\n[*] --> Idle\nIdle --> Busy: go\nBusy --> [*]",
		"flowchart TD\nA[日本語] --> B[😀]",
		"flowchart LR\nA --> B\nB[" + string(make([]byte, 60)) + "]",
		"flowchart TD\nA((x)) --> B([y]) --> C[[z]] --> D[(w)] --> E{{v}}",
	} {
		f.Add(s, 80)
	}
	f.Fuzz(func(t *testing.T, src string, width int) {
		if width > 400 {
			width = 400
		}
		lines, ok := Render(src, width)
		if !ok {
			return
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("line %q is %d cells wide, over the %d given", l, w, width)
			}
		}
	})
}
