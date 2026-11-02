package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// A turn the harness stops (the tool-call limit, the iteration limit, the
// cost budget) used to end with no message at all, and the person had to
// ask "is it done?" to learn that it was not. hero_agent's step_limit.rs
// (#869: a chat spent 50 steps and 1.9M input tokens and said nothing)
// gives such a turn a closing reply: Done, Left, and how to go on. For
// the tool-call and iteration limits one model call writes the Done and
// Left sections from the conversation, with the tools it may not call
// named in the instruction (the request keeps the tool list so the
// provider's prefix cache still serves the history, as compaction does).
// When the stop is the cost budget, or the call fails, is cut off after
// closeTimeout or returns nothing, the reply is built from the turn's
// facts alone: changed files, the last check's result, the todo list. The
// "how to continue" line is always ours, never the model's.
// JANETCODE_OFF=steplimit turns it off.

const continueHint = "Send **continue** and I will pick up from here — the work above is already in place."

const closingInstruction = "[harness] This turn has reached its limit and is stopping now: you cannot call any more tools. " +
	"Write the closing message to the user in two short sections: **Done**: what this turn actually accomplished (files changed, checks run and their result), " +
	"and **Left**: what is still unfinished or unverified, most important first. Be concrete and honest; do not claim anything you did not verify. " +
	"Do not tell the user how to continue; that line is added for you. Text only."

// closeTimeout bounds the closing call so a hung provider cannot hold a
// stopped turn open.
const closeTimeout = 90 * time.Second

// limitFacts is what the deterministic reply is built from.
type limitFacts struct {
	why       string // "its tool-call limit (50 calls)"
	edited    []string
	verdict   Verdict
	lastCheck string
	todos     []Todo
}

// turnStart is the index of the message that opened the current turn:
// the last user message that is not the harness's.
func turnStart(msgs []llm.Message) int {
	start := 0
	for i, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && !b.Synthetic && strings.TrimSpace(b.Text) != "" {
				start = i
			}
		}
	}
	return start
}

// editedFiles lists, in order and once each, the files the turn's edit
// calls named.
func editedFiles(msgs []llm.Message) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs[turnStart(msgs):] {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse || !editTool(b.Name) {
				continue
			}
			if p := pathArg(b.Input); p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// deterministicClosing is the closing reply with no model call.
func deterministicClosing(f limitFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "I stopped because this turn reached %s before the task was finished.\n\n**Done**\n", f.why)
	for _, t := range f.todos {
		if t.Done() {
			fmt.Fprintf(&b, "- %s\n", t.Text)
		}
	}
	if len(f.edited) == 0 {
		b.WriteString("- No files were changed.\n")
	} else {
		fmt.Fprintf(&b, "- Changed: %s\n", strings.Join(f.edited, ", "))
	}
	switch {
	case f.verdict == VerdictCheckFailed || f.verdict == VerdictVacuous:
		fmt.Fprintf(&b, "- The last check failed: `%s`\n", textfmt.TruncateTo(f.lastCheck, 120))
	case f.verdict == VerdictVerified:
		b.WriteString("- The last check that ran passed.\n")
	case len(f.edited) > 0:
		b.WriteString("- Nothing has verified these changes yet.\n")
	}
	b.WriteString("\n**Left**\n")
	open := openTodos(f.todos)
	if len(open) == 0 {
		b.WriteString("- The remaining work was not written down as a plan; ")
		if f.verdict == VerdictCheckFailed {
			b.WriteString("start with the failing check above.\n")
		} else {
			b.WriteString("check the changes against the request and finish what is missing.\n")
		}
	}
	for _, t := range open {
		fmt.Fprintf(&b, "- %s (%s)\n", t.Text, strings.ReplaceAll(t.Status, "_", " "))
	}
	b.WriteString("\n" + continueHint)
	return b.String()
}

// limitReason names the stop for the reply's opening line, and whether a
// model call may write the rest. ok is false for errors that are not a
// harness stop.
func (a *Agent) limitReason(err error) (why string, modelOK, ok bool) {
	switch {
	case errors.Is(err, ErrToolLimit):
		return fmt.Sprintf("its tool-call limit (%d calls)", a.MaxToolCalls), true, true
	case errors.Is(err, ErrMaxIterations):
		return fmt.Sprintf("its step limit (%d model calls)", a.Settings().MaxIterations), true, true
	case errors.Is(err, ErrBudget):
		return "its cost budget", false, true
	}
	return "", false, false
}

// closeOnLimit gives a turn stopped by err its closing reply, stored in
// the history and shown as the turn's last message. The history must end
// with the answers to every tool call, which the stops all guarantee.
func (a *Agent) closeOnLimit(ctx context.Context, err error, emit func(Event)) {
	why, modelOK, ok := a.limitReason(err)
	if !ok || a.Off["steplimit"] || ctx.Err() != nil || len(a.Messages) == 0 {
		return
	}
	if last := a.Messages[len(a.Messages)-1]; last.Role != llm.RoleUser {
		return // an unanswered assistant message: nothing sound to append to
	}
	outcome := a.Outcome()
	facts := limitFacts{why: why, edited: editedFiles(a.Messages), verdict: outcome.Verdict, todos: a.Todos()}
	if n := len(outcome.Checks); n > 0 {
		facts.lastCheck = outcome.Checks[n-1]
	}
	reply := ""
	if modelOK {
		reply = a.closingFromModel(ctx, emit)
	}
	if reply == "" {
		reply = deterministicClosing(facts)
	} else {
		reply += "\n\n" + continueHint
	}
	msg := llm.TextMessage(llm.RoleAssistant, reply)
	a.appendMessage(msg)
	a.integ.lastLen = len(a.Messages)
	a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &msg})
	emit(Event{Kind: EvText, Text: reply})
	emit(Event{Kind: EvTextBlockDone})
}

// closingFromModel makes the one closing call; "" on any failure.
func (a *Agent) closingFromModel(ctx context.Context, emit func(Event)) string {
	s := a.Settings()
	if s.Provider == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	msgs := append(append([]llm.Message{}, a.Messages...), llm.TextMessage(llm.RoleUser, closingInstruction))
	resp, err := s.Provider.Stream(ctx, llm.Request{
		Model: s.Model, System: a.systemPrompt(), Messages: msgs, Tools: a.turnTools(),
		MaxTokens: s.MaxTokens, Effort: s.Effort,
	}, nil)
	if resp != nil {
		a.addUsage(s.Model, resp.Usage)
	}
	if err != nil || resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Message.Text())
}
