package session

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// QueryFilter selects events across one session log.
type QueryFilter struct {
	Surface SurfaceKind // empty means any
	Text    string
	Tool    string
}

// EventHit is one matching event.
type EventHit struct {
	Event   Event
	Kind    SurfaceKind
	Snippet string
}

// QueryEvents filters one session's log.
func QueryEvents(events []Event, f QueryFilter) []EventHit {
	words := strings.Fields(strings.ToLower(f.Text))
	var out []EventHit
	for _, e := range events {
		k := Classify(events, e)
		if f.Surface != "" && f.Surface != "any" && k != f.Surface {
			continue
		}
		if f.Tool != "" && !strings.EqualFold(e.Name, f.Tool) {
			continue
		}
		blob := strings.ToLower(e.Output + " " + e.Arguments + " " + e.Name)
		if e.Message != nil {
			blob += " " + strings.ToLower(e.Message.Text())
		}
		if len(words) > 0 {
			ok := true
			for _, w := range words {
				if !strings.Contains(blob, w) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		snip := e.Output
		if snip == "" && e.Message != nil {
			snip = e.Message.Text()
		}
		if len(snip) > 120 {
			snip = snip[:117] + "…"
		}
		out = append(out, EventHit{Event: e, Kind: k, Snippet: snip})
	}
	return out
}

// LastAssistantSeq is the seq of the most recent assistant/message, or 0.
func LastAssistantSeq(events []Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == TypeAssistant {
			return events[i].Seq
		}
	}
	return 0
}

// MessageAtSeq returns the surface message at seq if any.
func MessageAtSeq(events []Event, seq int) *llm.Message {
	for i := range events {
		if events[i].Seq == seq {
			return events[i].Message
		}
	}
	return nil
}
