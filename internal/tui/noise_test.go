package tui

import (
	"strings"
	"testing"
)

// A bare "." a model emits between tool calls is not an answer: it must
// not leave a lone dot in the transcript, while a real one-word or
// emoji reply stays.
func TestPunctuationOnlyTextIsDropped(t *testing.T) {
	tr := newTranscript()
	assistantSaid(tr, ".\n\n")
	for _, e := range tr.entries {
		if e.kind == entryAssistant {
			t.Fatalf("a punctuation-only block was kept: %q", e.text)
		}
	}
	for _, keep := range []string{"ok", "👍", "42"} {
		tr := newTranscript()
		assistantSaid(tr, keep)
		out := plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
		if !strings.Contains(out, keep) {
			t.Fatalf("%q was dropped:\n%s", keep, out)
		}
	}
}
