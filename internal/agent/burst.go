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
// burst's first calls are ordinary work. A burst is usually one group of
// calls written over and over (3 to 9 distinct calls in 24 on d-cronexpr),
// so the reply is also ended at its first call that repeats an earlier one
// exactly, keeping the calls before it. JANETCODE_OFF=burstcap turns it
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
	// full is set at the call past the cap or the first repeat; what
	// streams after it until the cancel lands is not kept.
	full bool
	// repeat is set when the reply was ended because its latest call
	// repeated an earlier one; that call is dropped.
	repeat bool
	seen   map[string]bool
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
		if w.repeats() {
			w.full, w.repeat = true, true
			return true
		}
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

// repeats reports whether the latest call, complete now that another
// starts, is the same tool with the same arguments as an earlier one.
func (w *burstWatch) repeats() bool {
	n := len(w.calls)
	if n == 0 {
		return false
	}
	key := w.calls[n-1].Name + "\x00" + strings.TrimSpace(w.args[n-1].String())
	if w.seen[key] {
		return true
	}
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	w.seen[key] = true
	return false
}

// reply is the reply cut down to the calls that arrived whole: the
// longest prefix whose arguments are a complete JSON object.
func (w *burstWatch) reply(model string) *llm.Response {
	var blocks []llm.Block
	if t := strings.TrimSpace(w.text.String()); t != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
	}
	calls := w.calls
	if w.repeat {
		calls = calls[:len(calls)-1]
	}
	for i, c := range calls {
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
func burstNote(ran int, repeat bool) string {
	if repeat {
		return fmt.Sprintf("[harness] Your last reply started repeating calls it had already made, so it was stopped: its first %d calls were run once each, and the rest were dropped. Look at these results before calling anything again.", ran)
	}
	return fmt.Sprintf("[harness] Your last reply kept starting tool calls, so it was stopped: only its first %d calls were run, and the rest were dropped. Look at these results first; send only the calls you still need, a few at a time.", ran)
}
