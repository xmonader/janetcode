package agent

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// Steer queues a note for the running turn. The history belongs to the
// turn goroutine while it runs, so the note is held here and delivered by
// that goroutine before its next model call — which is the difference
// between steering a turn and starting another one.
func (a *Agent) Steer(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.steerMu.Lock()
	a.steer = append(a.steer, text)
	a.steerMu.Unlock()
}

// deliverSteering hands any queued notes to the model. It runs on the
// turn goroutine, just before the call that will read them.
func (a *Agent) deliverSteering(emit func(Event)) {
	a.steerMu.Lock()
	notes, harness := a.steer, a.harness
	a.steer, a.harness = nil, nil
	a.steerMu.Unlock()
	if len(harness) > 0 {
		// Harness text — a path-scoped rule this turn brought in — is
		// delivered the same way but marked synthetic, so nothing
		// downstream mistakes it for the user's words.
		a.deliverText(strings.Join(harness, "\n\n"), true)
	}
	if len(notes) == 0 {
		return
	}
	// A note the user typed mid-turn is a decision landing that the
	// mechanical-streak policy cannot see coming, so the rest of the
	// turn stays on Model rather than risk a cheap model missing what
	// changed.
	a.stepRoute.taint("the user steered this turn")
	text := strings.Join(notes, "\n\n")
	a.deliverText(text, false)
	emit(Event{Kind: EvNotice, Text: "steering the turn: " + strings.SplitN(text, "\n", 2)[0]})
}

// deliverText adds text to the conversation for the next model call. It
// rides with the trailing tool results when there are any: two user
// messages in a row are not something every provider accepts.
func (a *Agent) deliverText(text string, synthetic bool) {
	n := len(a.Messages)
	if n > 0 && a.Messages[n-1].Role == llm.RoleUser {
		a.appendToLast(llm.Block{Type: llm.BlockText, Text: text, Synthetic: synthetic})
		a.logUserMessage(true)
		return
	}
	if synthetic {
		a.appendHarnessText(text)
		return
	}
	a.appendUserText(text)
}

// appendUserText adds text as a new user message. It never mutates a message
// already sent to the provider: editing a cached prefix (especially a tool-
// result turn) makes DashScope/OpenRouter re-bill the whole history as fresh.
func (a *Agent) appendUserText(text string) {
	a.logUserMessage(a.appendUserBlocks(llm.Block{Type: llm.BlockText, Text: text}))
}

// appendHarnessText adds text the harness itself wrote — a correction, a
// check's output, a job update — as a user message marked synthetic, so
// nothing downstream mistakes it for something the user said.
func (a *Agent) appendHarnessText(text string) {
	a.logUserMessage(a.appendUserBlocks(llm.Block{Type: llm.BlockText, Text: text, Synthetic: true}))
}

// AddContext records text the user produced outside the model's view (a
// shell command they ran, a note) as user content for the next turn. It
// mutates the history and is only safe between turns; use Steer to say
// something to a turn that is already running.
func (a *Agent) AddContext(text string) { a.appendUserText(text) }

// AddHarnessContext is AddContext for text the UI's own machinery
// produces (a finished background job), not the user.
func (a *Agent) AddHarnessContext(text string) { a.appendHarnessText(text) }

// appendUserBlocks adds a user turn, reporting whether it merged into the
// trailing message rather than making a new one.
func (a *Agent) appendUserBlocks(blocks ...llm.Block) bool {
	if len(blocks) == 0 {
		return false
	}
	n := len(a.Messages)
	// Merge only into a trailing plain-text user turn (typed input before
	// the first model call). Tool-result messages have already gone over
	// the wire; appending to them would change bytes the prompt cache keyed on.
	if n > 0 && a.Messages[n-1].Role == llm.RoleUser && !hasToolResult(a.Messages[n-1]) {
		a.appendToLast(blocks...)
		return true
	}
	a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: blocks})
	return false
}

// logUserMessage records the trailing user message on the log. When the
// blocks merged into a message that is already logged (input typed after a
// failed call, or a note added before the turn ran) the event replaces that
// one, so the fold holds the grown message once instead of twice.
func (a *Agent) logUserMessage(merged bool) {
	n := len(a.Messages)
	if a.Log == nil || n == 0 {
		return
	}
	msg := a.Messages[n-1]
	e := session.Event{Type: session.TypeUserMessage, Surface: true, Message: &msg}
	if merged {
		if seqs := session.CurrentSeqs(a.Log.Events()); len(seqs) == n {
			e.SurfaceOp = session.SurfaceOpReplace
			e.Replaces = []int{seqs[n-1]}
		}
	}
	a.logEvent(e)
}

// reportJobs tells the model (and the user) about background jobs that
// ended since the last check.
func (a *Agent) reportJobs(emit func(Event)) {
	for _, n := range a.Jobs.Finished() {
		emit(Event{Kind: EvNotice, Text: strings.SplitN(n, "\n", 2)[0]})
		a.appendHarnessText("[background job update]\n" + n)
	}
}
