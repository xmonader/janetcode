package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// Request integrity: before every model call, check the outgoing history
// still carries the user's current request and did not shrink except
// through compaction. A failure is reported loudly (debug log and a notice
// event) but never blocks the call: a wrong warning costs a line, a
// silently broken request costs the turn.

type integrity struct {
	userText string // the current request, set by Run
	lastLen  int    // message count after the previous call's reply was appended
	shrunkOK bool   // compaction or pruning legitimately shortened history
}

func (a *Agent) checkIntegrity(emit func(Event)) {
	in := &a.integ
	var problems []string
	if n := len(a.Messages); in.lastLen > 0 && n < in.lastLen && !in.shrunkOK {
		problems = append(problems, fmt.Sprintf("history shrank from %d to %d messages without compaction", in.lastLen, n))
	}
	in.shrunkOK = false
	if want := prefix(in.userText, 120); want != "" && !historyContains(a.Messages, want) {
		problems = append(problems, "the current user request is missing from the outgoing messages")
	}
	if len(a.Messages) == 0 || a.Messages[len(a.Messages)-1].Role != llm.RoleUser {
		problems = append(problems, "the outgoing history does not end with a user message")
	}
	for _, p := range problems {
		a.debugf("integrity: %s", p)
		emit(Event{Kind: EvNotice, Text: "request integrity: " + p})
	}
}

// prefix is the first n bytes of s, cut back to a rune boundary. The
// results go into the request the model reads — the goal and the verbatim
// user asks of the working-memory record — and a request cut mid-rune
// arrives as a replacement character, so the cut never splits one.
func prefix(s string, n int) string {
	s = strings.TrimSpace(s)
	return s[:runeStartAtOrBefore(s, n)]
}

func historyContains(msgs []llm.Message, want string) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, b := range msgs[i].Blocks {
			if b.Type == llm.BlockText && strings.Contains(b.Text, want) {
				return true
			}
		}
	}
	return false
}
