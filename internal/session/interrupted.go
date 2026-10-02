package session

import (
	"encoding/json"

	"github.com/xmonader/janetcode/internal/llm"
)

// TurnLogsEnd is the Extra a turn/start carries when its writer logs a
// turn/end on every exit but a crash (see docs/notes/durable-turns.md).
// Only such a turn, started and never ended, is reported as interrupted:
// a session written before the marker logged no end on an error or a
// cancel, and reading those as crashes would offer to continue turns the
// user had stopped on purpose.
var TurnLogsEnd = json.RawMessage(`{"logs_end":true}`)

// TurnEndInterrupted is the turn/end reason the continuation writes for
// the turn the session died in, so it is reported once.
const TurnEndInterrupted = "interrupted"

// PendingCall is one tool call of the step an interrupted turn died in,
// and what the log says happened to it.
type PendingCall struct {
	ID      string // the harness's id (tool/call and tool/result carry it)
	ReplyID string // the id the result goes back under
	Name    string
	Input   json.RawMessage
	Started bool // approved and begun: tool/call was logged
	Done    bool // finished: its log-only tool/result was logged
	Output  string
	IsError bool
}

// Interrupted describes a turn the session died in.
type Interrupted struct {
	Turn int
	// Calls is the last step's tool calls when the model had asked for
	// tools and their results never reached the history; empty when the
	// session died waiting for a model call.
	Calls []PendingCall
}

// FindInterrupted reports the last turn when it started with the
// TurnLogsEnd marker and never ended, and the history it left can be
// carried on: its last model-visible message is a user message (the
// prompt or a step's results, waiting for a model call) or an assistant
// message whose tool calls were never answered. A turn that died after its
// final answer — in the checks that follow it — has nothing to continue.
func FindInterrupted(events []Event) *Interrupted {
	turn := -1
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type == TypeTurnEnd && turn < 0 {
			return nil // the newest turn event is an end
		}
		if e.Type == TypeTurnStart {
			if string(e.Extra) != string(TurnLogsEnd) {
				return nil
			}
			turn = e.Turn
			break
		}
	}
	if turn < 0 {
		return nil
	}
	idx := live(events)
	if len(idx) == 0 {
		return nil
	}
	last := events[idx[len(idx)-1]].Message
	switch {
	case last.Role == llm.RoleUser:
		return &Interrupted{Turn: turn}
	case last.Role == llm.RoleAssistant && len(last.ToolUses()) > 0:
		states := callStates(events)
		it := &Interrupted{Turn: turn}
		for _, u := range last.ToolUses() {
			pc := PendingCall{ID: u.ID, ReplyID: replyID(u), Name: u.Name, Input: u.Input}
			if st := states[u.ID]; st != nil {
				pc.Started = st.started
				if st.result != nil {
					pc.Done, pc.Output, pc.IsError = true, st.result.Output, st.result.IsError
				}
			}
			it.Calls = append(it.Calls, pc)
		}
		return it
	}
	return nil
}
