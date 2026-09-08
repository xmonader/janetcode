package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// DefaultCompactAt is the estimated input-token count at which the history
// is summarised before the next model call. It is deliberately well below
// any modern context window: past this size every call costs more, cache
// hits get rarer and models attend worse, so the working context is kept
// small regardless of how much the model could take. Models with smaller
// windows lower it further on the first overflow (see windowFromError).
const DefaultCompactAt = 80_000

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

const (
	// tailMessages and tailTokens bound the recent history kept verbatim
	// when the rest is summarised.
	tailMessages = 6
	tailTokens   = 12_000
	// minHeadChars is the smallest history worth summarising separately
	// from the kept tail.
	minHeadChars = 2_000
	// pruneKeep is the byte budget for an old tool result after pruning.
	pruneKeep = 2_000
	// superseded replaces the output of a read-only call repeated later
	// with identical input.
	superseded = "[output omitted: the same call was repeated later, see its newer result]"
)

// Prune applies lossless-in-practice tiers to the history before a
// summary is paid for: earlier results of read-only calls repeated with
// identical input are stubbed out, and old tool outputs are trimmed to
// their head and tail. The recent tail is untouched. It returns the
// number of bytes removed. Pruning is only done here, on the way to
// compaction, because rewriting old messages invalidates the cached
// request prefix.
func (a *Agent) Prune() int {
	n := len(a.Messages)
	tail := tailStart(a.Messages)
	// Latest occurrence of every read-only call, by tool and canonical input.
	latest := map[string]string{} // key → tool_use id
	readOnly := map[string]bool{}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse {
				continue
			}
			ro, seen := readOnly[b.Name]
			if !seen {
				t, ok := a.Tools.Get(b.Name)
				ro = ok && !t.Mutating()
				readOnly[b.Name] = ro
			}
			if ro {
				latest[b.Name+"\x00"+canonical(b.Input)] = b.ID
			}
		}
	}
	keep := map[string]bool{}
	for _, id := range latest {
		keep[id] = true
	}
	useKey := map[string]string{}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && readOnly[b.Name] {
				useKey[b.ID] = b.Name + "\x00" + canonical(b.Input)
			}
		}
	}
	saved := 0
	for i := 0; i < tail && i < n; i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolResult {
				continue
			}
			before := len(b.Text)
			switch {
			case useKey[b.ToolUseID] != "" && !keep[b.ToolUseID] && len(b.Text) > len(superseded):
				a.Messages[i].Blocks[j].Text = superseded
			case len(b.Text) > pruneKeep:
				a.Messages[i].Blocks[j].Text = a.trimOld(b.ToolUseID, b.Text)
			}
			saved += before - len(a.Messages[i].Blocks[j].Text)
		}
	}
	if saved > 0 {
		a.lastInputTokens, a.lastInputMsgs = 0, 0
		a.integ.shrunkOK = true
	}
	return saved
}

// canonical normalises tool input JSON so formatting differences do not
// defeat deduplication.
func canonical(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// tailStart returns the index of the first message of the recent tail:
// the latest assistant message such that the tail is within
// tailMessages and tailTokens, so tool results always follow their
// tool_use. It returns len(messages) when nothing qualifies.
func tailStart(msgs []llm.Message) int {
	start := len(msgs)
	tokens := 0
	for i := len(msgs) - 1; i >= 0 && len(msgs)-i <= tailMessages; i-- {
		tokens += messageChars(msgs[i]) / 4
		if tokens > tailTokens {
			break
		}
		if msgs[i].Role == llm.RoleAssistant {
			start = i
		}
	}
	return start
}

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

// Compact shrinks the history: first by pruning (see Prune), and when
// that is not enough by replacing everything but a recent tail with a
// model-written summary. The trailing user text message, if any, is kept
// so the pending request is not lost. force skips the "pruning was
// enough" shortcut.
func (a *Agent) Compact(ctx context.Context, emit func(Event)) error {
	return a.compact(ctx, emit, false)
}

// ForceCompact always writes a summary, for an explicit user request.
func (a *Agent) ForceCompact(ctx context.Context, emit func(Event)) error {
	return a.compact(ctx, emit, true)
}

func (a *Agent) compact(ctx context.Context, emit func(Event), force bool) error {
	if emit == nil {
		emit = func(Event) {}
	}
	if len(a.Messages) == 0 {
		return ErrNothingToCompact
	}
	before := a.EstimateTokens()
	if saved := a.Prune(); saved > 0 && !force {
		if limit := a.CompactThreshold(); limit > 0 && a.EstimateTokens() < limit*3/4 {
			emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context pruned: ~%d → ~%d tokens (old tool output trimmed, no summary needed)", before, a.EstimateTokens())})
			return nil
		}
	}

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
	// Keep the recent tail verbatim; summarise what came before it. A tiny
	// head is not worth a summary of its own: summarise it all.
	ts := tailStart(history)
	if ts == 0 || chars(history[:ts]) < minHeadChars {
		ts = len(history)
	}
	head, tail := history[:ts], history[ts:]
	midTurn := len(tail) == 0 && history[len(history)-1].Role == llm.RoleUser // ends with tool results

	req := llm.Request{
		Model:     a.Model,
		System:    compactSystem,
		Messages:  append(append([]llm.Message{}, head...), llm.TextMessage(llm.RoleUser, "Write the handoff note now.")),
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
	a.Messages = append([]llm.Message{llm.TextMessage(llm.RoleUser, note)}, tail...)
	if pending != nil {
		a.appendUserBlocks(pending.Blocks...)
	}
	a.lastInputTokens = 0
	a.lastInputMsgs = 0
	a.integ.shrunkOK = true
	after := a.EstimateTokens()
	emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context compacted: ~%d → ~%d tokens (%d recent messages kept)", before, after, len(tail))})
	return nil
}

func chars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += messageChars(m)
	}
	return n
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

var windowRe = regexp.MustCompile(`(?i)(?:>\s*|maximum context length is |context length of |limit of |window of )(\d{4,7})(?:\s*(?:tokens|maximum|max))?`)

// windowFromError pulls the model's context limit out of an overflow error
// such as "prompt is too long: 210000 tokens > 200000 maximum", or 0.
func windowFromError(err error) int {
	if err == nil {
		return 0
	}
	m := windowRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// trimOld shortens an old tool result to pruneKeep bytes. The full text is
// archived first (unless it already was) so the model can get it back.
func (a *Agent) trimOld(toolUse, text string) string {
	if a.Archive == nil {
		return tool.TruncateTo(text, pruneKeep)
	}
	if id := a.Archive.IDFor(toolUse); id != "" {
		// Already archived: keep the note that names the id, trim the rest.
		if i := strings.Index(text, "\n"); i > 0 && strings.HasPrefix(text, "[output is ") {
			return text[:i+1] + tool.TruncateTo(text[i+1:], pruneKeep)
		}
		return archiveNote(id, len(text), pruneKeep) + tool.TruncateTo(text, pruneKeep)
	}
	id, err := a.Archive.Put(toolUse, "", text)
	if err != nil {
		return tool.TruncateTo(text, pruneKeep)
	}
	return archiveNote(id, len(text), pruneKeep) + tool.TruncateTo(text, pruneKeep)
}
