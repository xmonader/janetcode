package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// A reply that starts more than maxBurstCalls tool calls is ended at the
// next one, and the complete calls it already streamed are run. Healthy
// replies on effbench made at most 15 calls; MiMo sometimes fell into a
// burst of 37 to 392 (mostly distinct one-off bash checks) that ran until
// the output limit or the task's time cap, every call then refused as
// possibly cut short: up to twelve minutes for nothing. Ended early, the
// burst's first calls are ordinary work. JANETCODE_OFF=burstcap turns it
// off.

// maxBurstCalls is how many tool calls of one reply are run.
const maxBurstCalls = 24

// errBurstCap is the cause a call is cancelled with when its reply
// started one tool call more than maxBurstCalls.
var errBurstCap = errors.New("the reply started too many tool calls")

// burstWatch reassembles a reply's tool calls from its stream. Calls
// arrive one after another, so a delta belongs to the latest call.
type burstWatch struct {
	text  strings.Builder
	calls []llm.Block
	args  []strings.Builder
	// full is set at the call past the cap; what streams after it until
	// the cancel lands belongs to that call, not the last one kept.
	full bool
}

func (a *Agent) newBurstWatch() *burstWatch {
	if a.Off["burstcap"] {
		return nil
	}
	return &burstWatch{}
}

// note takes one stream event and reports whether the call should end.
func (w *burstWatch) note(e llm.Event) bool {
	if w == nil || w.full {
		return false
	}
	switch e.Type {
	case llm.EventTextDelta:
		if len(w.calls) == 0 {
			w.text.WriteString(e.Text)
		}
	case llm.EventToolUseStart:
		if len(w.calls) == maxBurstCalls {
			w.full = true
			return true
		}
		if e.Block != nil {
			w.calls = append(w.calls, *e.Block)
			w.args = append(w.args, strings.Builder{})
		}
	case llm.EventToolUseDelta:
		if n := len(w.args); n > 0 {
			w.args[n-1].WriteString(e.Text)
		}
	}
	return false
}

// reply is the reply cut down to the calls that arrived whole: the
// longest prefix whose arguments are a complete JSON object.
func (w *burstWatch) reply(model string) *llm.Response {
	var blocks []llm.Block
	if t := strings.TrimSpace(w.text.String()); t != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
	}
	for i, c := range w.calls {
		in := strings.TrimSpace(w.args[i].String())
		if in == "" {
			in = "{}"
		}
		var obj map[string]any
		if json.Unmarshal([]byte(in), &obj) != nil {
			break
		}
		c.Type = llm.BlockToolUse
		c.Input = json.RawMessage(in)
		blocks = append(blocks, c)
	}
	stop := llm.StopToolUse
	if len(blocks) == 0 || blocks[len(blocks)-1].Type != llm.BlockToolUse {
		// Nothing arrived whole: a reply cut short, as at the output limit.
		stop = llm.StopMaxTokens
	}
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Blocks: blocks},
		StopReason: stop,
		Model:      model,
	}
}

// burstNote is what the model is told with the results of a cut reply.
func burstNote(ran int) string {
	return fmt.Sprintf("[harness] Your last reply kept starting tool calls, so it was stopped: only its first %d calls were run, and the rest were dropped. Look at these results first; send only the calls you still need, a few at a time.", ran)
}
