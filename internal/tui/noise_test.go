package tui

import (
	"encoding/json"
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

// Models put "cd <workspace> &&" in front of every command. It changes
// nothing about what runs, and in the tool row it filled the width with
// the workspace path before the command itself was reached.
func TestBashRowDropsTheWorkspaceCd(t *testing.T) {
	cwd := "/home/u/proj/rs-csvstat"
	for _, cmd := range []string{
		`cd /home/u/proj/rs-csvstat && cargo test 2>&1`,
		`cd "/home/u/proj/rs-csvstat"; cargo test 2>&1`,
	} {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		if got := summarizeInput("bash", string(raw), 80, cwd); got != "cargo test 2>&1" {
			t.Errorf("summary of %q = %q", cmd, got)
		}
	}
	raw, _ := json.Marshal(map[string]string{"command": "cd sub && make"})
	if got := summarizeInput("bash", string(raw), 80, cwd); got != "cd sub && make" {
		t.Errorf("a cd elsewhere is part of the command: %q", got)
	}
}
