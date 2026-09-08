package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// DefaultCompactAt is the estimated input-token count at which the history
// is summarised before the next model call.
const DefaultCompactAt = 150_000

// EvCompacted reports that the history was summarised. Text carries a
// human-readable note.
const EvCompacted EventKind = "compacted"

const compactSystem = `You are compacting a coding-agent conversation so work can continue in a fresh context. Write a handoff note for another engineer-agent that will only see this note. Include, with concrete file paths, commands and names:
1. The user's goal and any constraints or preferences they stated.
2. What has been done so far and what was verified (tests, builds, outputs).
3. What is in progress or still to do, in order.
4. Key facts learned about the codebase that would be expensive to rediscover.
5. Mistakes made or approaches ruled out, so they are not repeated.
Be precise and complete rather than brief. Do not add commentary; output only the note.`

// ErrNothingToCompact is returned by Compact when the history is empty.
var ErrNothingToCompact = errors.New("agent: nothing to compact")

// EstimateTokens approximates the prompt size of the current history. It
// uses the last real input-token count when available and a bytes/4
// heuristic for what was added since.
func (a *Agent) EstimateTokens() int {
	est := len(a.System) / 4
	for _, d := range a.Tools.Defs() {
		est += (len(d.Description) + len(d.InputSchema)) / 4
	}
	for i, m := range a.Messages {
		if i < a.lastInputMsgs {
			continue
		}
		est += messageChars(m) / 4
	}
	return est + a.lastInputTokens
}

func messageChars(m llm.Message) int {
	n := 0
	for _, b := range m.Blocks {
		if b.Type == llm.BlockImage {
			// Providers bill images by area, roughly 1.5k tokens for a
			// screenshot; base64 length would overcount a hundredfold.
			n += 1600 * 4
			continue
		}
		n += len(b.Text) + len(b.Input) + len(b.Name) + 16
	}
	return n
}

// Compact replaces the history with a model-written summary. The trailing
// user text message, if any, is kept so the pending request is not lost.
func (a *Agent) Compact(ctx context.Context, emit func(Event)) error {
	if emit == nil {
		emit = func(Event) {}
	}
	if len(a.Messages) == 0 {
		return ErrNothingToCompact
	}
	before := a.EstimateTokens()

	// Detach a trailing plain user message so it survives verbatim.
	var pending *llm.Message
	history := a.Messages
	if last := history[len(history)-1]; last.Role == llm.RoleUser && !hasToolResult(last) {
		pending = &last
		history = history[:len(history)-1]
	}
	if len(history) == 0 {
		return ErrNothingToCompact
	}
	midTurn := history[len(history)-1].Role == llm.RoleUser // ends with tool results

	req := llm.Request{
		Model:     a.Model,
		System:    compactSystem,
		Messages:  append(append([]llm.Message{}, history...), llm.TextMessage(llm.RoleUser, "Write the handoff note now.")),
		MaxTokens: a.MaxTokens,
	}
	resp, err := a.Provider.Stream(ctx, req, nil)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	summary := strings.TrimSpace(resp.Message.Text())
	if summary == "" {
		return errors.New("compact: model returned an empty summary")
	}
	a.Usage.Add(resp.Usage)

	note := "[Context was compacted. Handoff note from the earlier part of this conversation:]\n\n" + summary
	if midTurn {
		note += "\n\n[The previous turn was interrupted mid-task by compaction. Continue the work described above.]"
	}
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, note)}
	if pending != nil {
		a.Messages[0].Blocks = append(a.Messages[0].Blocks, pending.Blocks...)
	}
	a.lastInputTokens = 0
	a.lastInputMsgs = 0
	after := a.EstimateTokens()
	emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context compacted: ~%d → ~%d tokens", before, after)})
	return nil
}

func hasToolResult(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
}

// isContextOverflow recognises the provider errors that mean the prompt no
// longer fits the model's window.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"prompt is too long",
		"context_length_exceeded",
		"maximum context length",
		"context window",
		"model_context_window_exceeded",
		"too many tokens",
		"input is too long",
		"exceeds the maximum",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
