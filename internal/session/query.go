package session

import (
	"strings"
	"unicode/utf8"
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
	hidden := shadowedSet(events)
	var out []EventHit
	for _, e := range events {
		k := ClassifyWith(hidden, e)
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
		snip = truncRunes(snip, 120)
		out = append(out, EventHit{Event: e, Kind: k, Snippet: snip})
	}
	return out
}

// truncRunes shortens s to at most max bytes without splitting a rune.
func truncRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
