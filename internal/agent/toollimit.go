package agent

import (
	"errors"
	"fmt"

	"github.com/xmonader/janetcode/internal/llm"
)

// ErrToolLimit is returned when a run has made MaxToolCalls tool calls.
// The headless runner maps it to exit 57.
var ErrToolLimit = errors.New("the run reached its tool-call limit")

// overToolLimit reports whether running uses would take the run past
// MaxToolCalls. The whole batch is judged at once: running two of three
// parallel calls would leave the model reading half its own plan.
func (a *Agent) overToolLimit(uses []llm.Block) (used int, over bool) {
	if a.MaxToolCalls <= 0 || len(uses) == 0 {
		return 0, false
	}
	used, _ = a.Stats().ToolCalls()
	return used, used+len(uses) > a.MaxToolCalls
}

// stopForToolLimit ends the turn on the cap, answering the calls unrun so
// the history stays paired.
func (a *Agent) stopForToolLimit(used int, uses []llm.Block, emit func(Event)) error {
	a.answerUnrun(uses, "the run's tool-call limit is reached", emit)
	emit(Event{Kind: EvVerdict, Text: "stopped on the tool-call limit", IsError: true})
	return fmt.Errorf("%w: %d made, %d more asked for, limit %d", ErrToolLimit, used, len(uses), a.MaxToolCalls)
}
