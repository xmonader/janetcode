package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// EventKind identifies what happened in the loop.
type EventKind string

const (
	EvText          EventKind = "text"           // Text: streamed assistant text
	EvThinking      EventKind = "thinking"       // Text: streamed reasoning
	EvToolStart     EventKind = "tool_start"     // Call: model began a tool call
	EvToolInput     EventKind = "tool_input"     // Text: partial tool input JSON
	EvToolRun       EventKind = "tool_run"       // Call: input complete, about to execute
	EvToolResult    EventKind = "tool_result"    // Call, Output, IsError, Duration
	EvTurnDone      EventKind = "turn_done"      // Usage, StopReason
	EvTextBlockDone EventKind = "text_block_end" // one assistant message finished streaming
	EvVerdict       EventKind = "verdict"        // Text: what the turn demonstrated
	// EvDiscard says the partial assistant text streamed so far is void
	// and must be taken off the screen: the call it came from failed and
	// is being retried on another model, so nothing of it reaches the
	// conversation either.
	EvDiscard EventKind = "discard"
	// EvPhase names what the turn is waiting on right now — a retry and
	// its countdown, a compaction, a subagent. It is a status line, never
	// a transcript entry, and the next event supersedes it.
	EvPhase EventKind = "phase"
	// EvRetry reports a model call that failed, was retried and then
	// succeeded. EvPhase already showed the countdown, but a status line
	// is gone the moment the next one lands: once the call answers, a
	// turn that cost four attempts and eight seconds of backoff looked
	// exactly like one that cost none. Retry carries what it took.
	EvRetry EventKind = "retry"
)

// Event is a loop notification for the UI.
type Event struct {
	Kind       EventKind
	Text       string
	Call       *ToolCall
	Output     string
	IsError    bool
	Duration   time.Duration
	Usage      llm.Usage
	StopReason llm.StopReason
	// Retry is set on EvRetry: what the model call had to survive.
	Retry *llm.Retry
	// Denied marks an EvToolResult that is a refusal rather than a
	// failure: a permission rule, a hook, read-only mode or the user
	// saying no. A caller counting denials cannot tell them apart from
	// the message alone -- each of those paths words it differently --
	// and a headless run reported zero denials for a call a --deny rule
	// had refused.
	Denied bool
	// Evidence is set on EvVerdict: the turn's checks on the one
	// evidence model (internal/evidence), so every surface renders the
	// same statuses and the same stale/blocked call as the session log.
	Evidence *evidence.Report
}

// serialize makes emit safe to call from tool goroutines.
func serialize(emit func(Event)) func(Event) {
	if emit == nil {
		return func(Event) {}
	}
	var mu sync.Mutex
	return func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}
}

// ledgered wraps emit so that every harness notice lands in the session
// ledger as well as on the screen. Without it a notice is a line that
// scrolls away, and /why — and anyone reading the session afterwards —
// has nothing to work from. It writes one small record per notice, of
// which a busy turn produces a handful.
func (a *Agent) ledgered(emit func(Event)) func(Event) {
	if emit == nil {
		emit = func(Event) {}
	}
	return func(e Event) {
		// A retry is ledgered too. It is a thing the harness did on its
		// own — it waited, and it bought the call twice — and it leaves
		// a "↻ retried 2× · rate limited · waited 1.5s" line in the
		// transcript that /why could not account for, because the only
		// record of it was that line.
		if (e.Kind == EvNotice || e.Kind == EvRetry) && strings.TrimSpace(e.Text) != "" {
			a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Step: a.stepN, Output: e.Text})
		}
		emit(e)
	}
}

// traced wraps emit with debug logging of every event.
func (a *Agent) traced(emit func(Event)) func(Event) {
	if a.Debug == nil {
		return emit
	}
	if emit == nil {
		emit = func(Event) {}
	}
	return func(e Event) {
		name := ""
		if e.Call != nil {
			name = " " + e.Call.Name
		}
		a.debugf("event %s%s len=%d", e.Kind, name, len(e.Text)+len(e.Output))
		emit(e)
	}
}
