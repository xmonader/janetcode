package session

import "strings"

// Call-id prefixes of tool calls made by a tool rather than by the model:
// execute_code's script, run_code's step list, and a recipe replay. Their
// ids and order are that mechanism's own implementation detail, so
// anything comparing what the model decided (replay, --what-if, eval tool
// use) leaves them out.
const (
	ScriptCallPrefix  = "script-"
	RunCodeCallPrefix = "runcode-"
	RecipeCallPrefix  = "recipe-"
)

// IsNestedCall reports whether callID belongs to a tool call made on the
// model's behalf by another tool (see ScriptCallPrefix and friends) rather
// than one the model asked for directly.
func IsNestedCall(callID string) bool {
	return strings.HasPrefix(callID, ScriptCallPrefix) ||
		strings.HasPrefix(callID, RunCodeCallPrefix) ||
		strings.HasPrefix(callID, RecipeCallPrefix)
}

// TurnPrompt is one recorded turn and the text that started it.
type TurnPrompt struct {
	Turn int
	Text string
}

// Turns reads a session's turns and their driving text from its event
// log: every TypeTurnStart marks one real call to the agent's Run (its
// Turn is the agent's own turn counter), and the first TypeUserMessage of
// each turn is the text that call was given.
//
// Anything that re-drives a recording through a replay provider must call
// Run exactly once per entry, in order. The provider serves recorded
// replies strictly in sequence, so a turn skipped (one whose prompt was
// synthetic: a /goal round, an undo note) or a turn added (a steering
// message typed while a turn was running is a second user message inside
// the same turn, not a turn of its own) desyncs every reply after it.
func Turns(events []Event) []TurnPrompt {
	maxTurn := 0
	for _, e := range events {
		if e.Type == TypeTurnStart && e.Turn > maxTurn {
			maxTurn = e.Turn
		}
	}
	if maxTurn == 0 {
		return nil
	}
	text := make([]string, maxTurn+1)
	seen := make([]bool, maxTurn+1)
	for _, e := range events {
		if e.Type != TypeUserMessage || e.Message == nil || e.Turn <= 0 || e.Turn > maxTurn || seen[e.Turn] {
			continue
		}
		seen[e.Turn] = true
		text[e.Turn] = e.Message.Text()
	}
	out := make([]TurnPrompt, 0, maxTurn)
	for t := 1; t <= maxTurn; t++ {
		out = append(out, TurnPrompt{Turn: t, Text: text[t]})
	}
	return out
}
