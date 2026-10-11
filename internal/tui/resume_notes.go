package tui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// noteLines caps how much of a harness note a resumed transcript shows:
// a shared "!" command carries its whole output, which the model needed
// and the reader does not.
const noteLines = 4

// historyUser replays one user-role text block of a resumed session.
// Only what the person typed comes back as a prompt row. A note the
// harness wrote on their behalf (an undo, a redo, a shared command)
// comes back as an info line saying what happened; other machinery —
// the repository map, recalled memory, skill bodies — is context the
// model read and is left out, as the web view does.
func (t *transcript) historyUser(b llm.Block) {
	text := strings.TrimSpace(b.Text)
	switch {
	case text == "":
	case session.HarnessNote(text):
		lines := strings.Split(text, "\n")
		if len(lines) > noteLines {
			lines = append(lines[:noteLines], "…")
		}
		t.info(strings.Join(lines, "\n"))
	case b.Synthetic, strings.HasPrefix(text, "[repository map:"):
	default:
		t.user(b.Text)
	}
}
