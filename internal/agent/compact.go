package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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

// pruneTo replaces old tool results with one-line placeholders until the
// estimate is at or under target tokens, or the recent tail is reached.
// Results go in attention order, least attended first: a result the model
// never referred to again (a grep it moved on from) goes before a file it
// kept editing, and a command that was re-run later goes first of all
// since its output is stale. Full outputs go to the archive first. It
// returns how many results were replaced.
func (a *Agent) pruneTo(target int) int {
	if target <= 0 || a.EstimateTokens() <= target {
		return 0
	}
	tail := tailStart(a.Messages)
	uses := map[string]llm.Block{}
	for _, m := range a.Messages[:min(tail, len(a.Messages))] {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse {
				uses[b.ID] = b
			}
		}
	}
	type candidate struct {
		i, j    int
		use     llm.Block
		lastRef int
	}
	var cands []candidate
	for i := 0; i < tail && i < len(a.Messages); i++ {
		for j, b := range a.Messages[i].Blocks {
			if b.Type != llm.BlockToolResult || strings.HasPrefix(b.Text, prunedPrefix) || b.Text == superseded {
				continue
			}
			u, ok := uses[b.ToolUseID]
			if !ok {
				continue
			}
			cands = append(cands, candidate{i: i, j: j, use: u, lastRef: a.lastReference(i, u)})
		}
	}
	sort.SliceStable(cands, func(x, y int) bool {
		if cands[x].lastRef != cands[y].lastRef {
			return cands[x].lastRef < cands[y].lastRef
		}
		return cands[x].i < cands[y].i
	})
	replaced := 0
	for _, c := range cands {
		b := a.Messages[c.i].Blocks[c.j]
		id := ""
		if a.Archive != nil {
			if id = a.Archive.IDFor(b.ToolUseID); id == "" {
				id, _ = a.Archive.Put(b.ToolUseID, c.use.Name, b.Text)
			}
		}
		ph := placeholder(c.use.Name, c.use.Input, b.Text, id)
		if len(ph) >= len(b.Text) {
			continue
		}
		a.Messages[c.i].Blocks[c.j].Text = ph
		replaced++
		a.lastInputTokens, a.lastInputMsgs = 0, 0
		a.integ.shrunkOK = true
		if a.EstimateTokens() <= target {
			return replaced
		}
	}
	return replaced
}

// lastReference is the index of the last message after at (the result's
// position) whose assistant text or tool inputs mention what the call at
// `use` was about: its path, pattern or command. A result nothing came
// back to keeps its own index; a bash command repeated verbatim later
// gets -1 because the earlier output is stale.
func (a *Agent) lastReference(at int, use llm.Block) int {
	subjects := subjectsOf(use.Name, use.Input)
	if len(subjects) == 0 {
		return at
	}
	last := at
	for i := len(a.Messages) - 1; i > at; i-- {
		m := a.Messages[i]
		for _, b := range m.Blocks {
			var text string
			switch b.Type {
			case llm.BlockToolUse:
				if use.Name == "bash" && b.Name == "bash" && canonical(b.Input) == canonical(use.Input) {
					return -1
				}
				text = string(b.Input)
			case llm.BlockText:
				if m.Role != llm.RoleAssistant {
					continue
				}
				text = b.Text
			default:
				continue
			}
			for _, s := range subjects {
				if strings.Contains(text, s) {
					return i
				}
			}
		}
	}
	return last
}

// subjectsOf names what a tool call was about, for reference matching.
func subjectsOf(name string, input json.RawMessage) []string {
	var in map[string]any
	json.Unmarshal(input, &in)
	var out []string
	for _, k := range []string{"path", "pattern", "command", "symbol", "query", "url"} {
		v, _ := in[k].(string)
		v = strings.TrimSpace(v)
		if len(v) < 3 || v == "." {
			continue
		}
		if k == "path" {
			v = strings.TrimSuffix(v, "/")
		}
		out = append(out, v)
	}
	return out
}

// summaryCooldown is how long compaction stops asking the model for a
// summary after a summary call failed (rate limit, outage): pruning alone
// carries the session meanwhile instead of re-hitting the provider.
const summaryCooldown = 10 * time.Minute

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
		tokens += messageTokens(msgs[i])
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
// uses the last real input-token count when available and a
// character-class estimate (see estimateTokens) for what was added since.
func (a *Agent) EstimateTokens() int {
	est := estimateTokens(a.systemPrompt())
	for _, d := range a.Tools.Defs() {
		est += estimateTokens(d.Description) + estimateTokens(string(d.InputSchema))
	}
	for i, m := range a.Messages {
		if i < a.lastInputMsgs {
			continue
		}
		est += messageTokens(m)
	}
	return est + a.lastInputTokens
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
	limit := a.CompactThreshold()
	if !force && !a.Off["prune"] {
		// Free the window without paying for a summary: dedupe and trim
		// first, then condense the oldest results to placeholders down to
		// half the threshold so the next few turns fit too.
		saved := a.Prune()
		replaced := 0
		if limit > 0 {
			replaced = a.pruneTo(limit / 2)
		}
		if (saved > 0 || replaced > 0) && limit > 0 && a.EstimateTokens() < limit*3/4 {
			emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context pruned: ~%d → ~%d tokens (%d old results condensed, no summary needed)", before, a.EstimateTokens(), replaced)})
			return nil
		}
		if since := time.Since(a.summaryFailedAt); a.summaryFailedAt.IsZero() == false && since < summaryCooldown {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf("context summary skipped: the last summary call failed %s ago; pruned to ~%d tokens instead", since.Round(time.Second), a.EstimateTokens())})
			return nil
		}
	}

	var hookNotes []string
	if a.Hooks.Has(hook.PreCompact) {
		hookNotes = a.Hooks.Run(ctx, hook.Input{Event: hook.PreCompact, Cwd: a.Hooks.Cwd, Tokens: before}).Notes
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

	typed := !a.Off["working"]
	req := llm.Request{Model: a.Model, System: compactSystem, MaxTokens: a.MaxTokens}
	ask := "Write the handoff note now."
	if typed {
		a.working.absorb(head)
		req.System = workingSystem
		ask = "Previous working memory (update it):\n" + a.working.modelSections() + "\n\nWrite the updated JSON object now."
	}
	req.Messages = append(append([]llm.Message{}, head...), llm.TextMessage(llm.RoleUser, ask))
	resp, err := a.Provider.Stream(ctx, req, nil)
	if err != nil {
		a.summaryFailedAt = time.Now()
		return fmt.Errorf("compact: %w", err)
	}
	summary := strings.TrimSpace(resp.Message.Text())
	if summary == "" {
		a.summaryFailedAt = time.Now()
		return errors.New("compact: model returned an empty summary")
	}
	a.summaryFailedAt = time.Time{}
	a.Usage.Add(resp.Usage)

	var shadowed []int
	if a.Log != nil {
		shadowed = session.CurrentSeqs(a.Log.Events())
		a.logEvent(session.Event{Type: session.TypeCompactStart, Turn: a.turnN})
	}
	note := "[Context was compacted. Handoff note from the earlier part of this conversation:]\n\n" + summary
	if typed {
		w := &a.working
		w.Latest = a.integ.userText
		if !w.merge(summary) {
			emit(Event{Kind: EvNotice, Text: "working memory: the model returned prose instead of the record; kept as notes"})
		}
		if a.Hooks != nil {
			w.hash(a.Hooks.Cwd)
		}
		note = w.Render()
	}
	for _, n := range hookNotes {
		note += "\n\n[From a pre_compact hook:]\n" + n
	}
	if midTurn {
		note += "\n\n[The previous turn was interrupted mid-task by compaction. Continue the work described above.]"
	}
	a.Messages = append([]llm.Message{llm.TextMessage(llm.RoleUser, note)}, tail...)
	a.RefreshExtra() // the prefix is rebuilt anyway; pick up new memory and skills
	if pending != nil {
		a.appendUserBlocks(pending.Blocks...)
	}
	a.lastInputTokens = 0
	a.lastInputMsgs = 0
	a.integ.shrunkOK = true
	after := a.EstimateTokens()
	a.stats.compacted()
	if a.Log != nil {
		sum := a.Messages[0]
		u := resp.Usage
		a.logEvent(session.Event{Type: session.TypeCompactSummary, Turn: a.turnN, Shadowed: shadowed, Tokens: before, Usage: &u})
		a.logEvent(session.Event{Type: session.TypeUserMessage, Turn: a.turnN, Surface: true, SurfaceOp: session.SurfaceOpReplace, Replaces: shadowed, Message: &sum})
		for i := 1; i < len(a.Messages); i++ {
			m := a.Messages[i]
			t := session.TypeUserMessage
			if m.Role == llm.RoleAssistant {
				t = session.TypeAssistant
			}
			a.logEvent(session.Event{Type: t, Turn: a.turnN, Surface: true, Message: &m})
		}
		a.logEvent(session.Event{Type: session.TypeCompactEnd, Turn: a.turnN})
	}
	emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context compacted: ~%d → ~%d tokens (%d recent messages kept)", before, after, len(tail))})
	return nil
}

func chars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			n += len(b.Text) + len(b.Input)
		}
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
