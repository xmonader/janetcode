package tui

import "testing"

// CRLF text pasted through tmux without -r arrives with each line feed
// turned into a carriage return, \r\r per line, and was double-spaced.
func TestCleanPasteCRPairs(t *testing.T) {
	for in, want := range map[string]string{
		"a\r\rb\r\rc":        "a\nb\nc",   // CRLF through tmux
		"a\r\r\r\rb":         "a\n\nb",    // with a blank line
		"a\rb\rc":            "a\nb\nc",   // LF through tmux
		"a\r\rb\rc":          "a\n\nb\nc", // mixed: a real blank line
		"a\r\nb\r\n\r\nc":    "a\nb\n\nc", // a terminal's CRLF
		"line one\nline two": "line one\nline two",
	} {
		if got := cleanPaste(in); got != want {
			t.Errorf("cleanPaste(%q) = %q, want %q", in, got, want)
		}
	}
}
