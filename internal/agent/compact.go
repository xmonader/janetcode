package agent

import (
	"bytes"
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
	label := map[string][2]string{} // tool_use id → name, subject
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse {
				continue
			}
			label[b.ID] = [2]string{b.Name, subjectOf(b.Name, b.Input)}
			if readOnly[b.Name] {
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
				a.Messages[i].Blocks[j].Text = a.trimOld(b.ToolUseID, label[b.ToolUseID][0], label[b.ToolUseID][1], b.Text)
			}
			saved += before - len(a.Messages[i].Blocks[j].Text)
		}
	}
	if saved > 0 {
		a.lastInputTokens, a.lastInputMsgs = 0, 0
		a.integ.shrunkOK, a.historyRewritten = true, true
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
				id, _ = a.Archive.PutSubject(b.ToolUseID, c.use.Name, subjectOf(c.use.Name, c.use.Input), b.Text)
			}
		}
		ph := placeholder(c.use.Name, c.use.Input, b.Text, id)
		if len(ph) >= len(b.Text) {
			continue
		}
		a.Messages[c.i].Blocks[c.j].Text = ph
		replaced++
		a.lastInputTokens, a.lastInputMsgs = 0, 0
		a.integ.shrunkOK, a.historyRewritten = true, true
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

// subjectOf is the first subject of a call, or "": the label an archive
// entry carries.
func subjectOf(name string, input json.RawMessage) string {
	if s := subjectsOf(name, input); len(s) > 0 {
		return s[0]
	}
	return ""
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

// TokenEstimate is the most recent EstimateTokens value. Unlike
// EstimateTokens it reads no message state, so the UI can call it from
// another goroutine while a turn is appending to the history.
func (a *Agent) TokenEstimate() int { return int(a.estimate.Load()) }

// EstimateTokens approximates the prompt size of the current history. It
// uses the last real input-token count when available and a
// character-class estimate (see estimateTokens) for what was added since.
// It must be called from the goroutine running the turn.
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
	est += a.lastInputTokens
	a.estimate.Store(int64(est))
	return est
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
		if saved > 0 || replaced > 0 {
			a.syncSurface()
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

	// The log seqs of the messages about to be replaced, aligned with
	// a.Messages by index (CurrentSeqs folds in model order).
	var surface []int
	if a.Log != nil {
		surface = session.CurrentSeqs(a.Log.Events())
		if len(surface) != len(a.Messages) {
			surface = nil // out of step: fall back to replacing the whole surface
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

	typed := !a.Off["working"]
	instruction := compactSystem + "\n\nWrite the handoff note now."
	if typed {
		a.working.absorb(head)
		instruction = workingSystem + "\n\nPrevious working memory (update it):\n" + a.working.modelSections() + "\n\nWrite the updated JSON object now."
	}
	var schema *llm.Schema
	if typed {
		schema = workingSchema
	}
	var summary string
	var usage llm.Usage
	if typed && !a.Off["richskip"] && a.working.rich(head) {
		// The harness observed enough of this head itself — which files
		// changed, what ran and how it exited — and the model-maintained
		// sections already exist from an earlier summary. Carry them
		// forward as they are instead of paying for a summary call.
		summary = a.working.modelSections()
	} else {
		resp, err := a.summarise(ctx, head, instruction, schema)
		if err != nil {
			a.summaryFailedAt = time.Now()
			return fmt.Errorf("compact: %w", err)
		}
		summary = strings.TrimSpace(resp.Message.Text())
		if summary == "" {
			a.summaryFailedAt = time.Now()
			return errors.New("compact: model returned an empty summary")
		}
		usage = resp.Usage
	}
	a.summaryFailedAt = time.Time{}
	a.addUsage(usage)

	if a.Log != nil {
		a.logEvent(session.Event{Type: session.TypeCompactStart, Turn: a.turnN})
	}
	note := "[Context was compacted. Handoff note from the earlier part of this conversation:]\n\n" + summary + "\n\n" + faultDiscipline
	if typed {
		w := &a.working
		w.Latest = a.integ.userText
		w.Todos = openTodos(a.Todos())
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
	// The head leaves the model's reach now; its text goes to the archive
	// first so that what the summary dropped — reasoning, tool inputs, the
	// small results that were never archived on their own — stays one
	// expand_output call away instead of being gone.
	if a.Archive != nil {
		if id, err := a.Archive.PutSubject(fmt.Sprintf("compaction-%d-%d", a.turnN, time.Now().UnixNano()), "compacted conversation", fmt.Sprintf("turn %d", a.turnN), renderMessages(head)); err == nil && id != "" {
			note += fmt.Sprintf("\n\nThe full text of the compacted conversation is archived as %s: expand_output {\"id\":%q,\"query\":\"<what you are looking for>\"} finds passages in it, offset/limit reads it in order.", id, id)
		}
	}
	a.Messages = append([]llm.Message{llm.TextMessage(llm.RoleUser, note)}, tail...)
	a.RefreshExtra() // the prefix is rebuilt anyway; pick up new memory and skills
	mergedPending := false
	if pending != nil {
		mergedPending = a.appendUserBlocks(pending.Blocks...)
	}
	a.lastInputTokens = 0
	a.lastInputMsgs = 0
	a.integ.shrunkOK, a.historyRewritten = true, true
	after := a.EstimateTokens()
	a.stats.compacted()
	if a.Log != nil {
		// The summary replaces the head (and the pending message when it was
		// merged into the summary). The tail is untouched: its events stay on
		// the surface where they are, so the log does not gain a second copy
		// of every kept message at each compaction.
		var shadowed []int
		relog := surface == nil
		if relog {
			shadowed = session.CurrentSeqs(a.Log.Events())
		} else {
			shadowed = append(shadowed, surface[:len(head)]...)
			if pending != nil && mergedPending {
				shadowed = append(shadowed, surface[len(surface)-1])
			}
		}
		sum := a.Messages[0]
		u := usage
		a.logEvent(session.Event{Type: session.TypeCompactSummary, Turn: a.turnN, Shadowed: shadowed, Tokens: before, Usage: &u})
		a.logEvent(session.Event{Type: session.TypeUserMessage, Turn: a.turnN, Surface: true, SurfaceOp: session.SurfaceOpReplace, Replaces: shadowed, Message: &sum})
		if relog {
			for i := 1; i < len(a.Messages); i++ {
				m := a.Messages[i]
				t := session.TypeUserMessage
				if m.Role == llm.RoleAssistant {
					t = session.TypeAssistant
				}
				a.logEvent(session.Event{Type: t, Turn: a.turnN, Surface: true, Message: &m})
			}
		}
		a.logEvent(session.Event{Type: session.TypeCompactEnd, Turn: a.turnN})
	}
	emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context compacted: ~%d → ~%d tokens (%d recent messages kept)", before, after, len(tail))})
	return nil
}

// summarise asks the model for the compaction summary of head. The call
// is a strict extension of the conversation's own requests — the same
// system prompt and tool schemas, the head verbatim, one instruction
// appended as the last user message — so a provider with prefix caching
// serves the replayed head from cache instead of re-billing it fresh,
// which was most of what a summary cost. A model that answers the
// instruction with a tool call instead of text gets one cold retry with
// the instruction as the system prompt and no tools.
func (a *Agent) summarise(ctx context.Context, head []llm.Message, instruction string, schema *llm.Schema) (*llm.Response, error) {
	msgs := append(append([]llm.Message{}, head...), llm.TextMessage(llm.RoleUser, instruction+"\n\nReply with text only; do not call tools."))
	resp, err := a.Provider.Stream(ctx, llm.Request{
		Model:     a.Model,
		System:    a.systemPrompt(),
		Messages:  msgs,
		Tools:     a.Tools.Defs(),
		MaxTokens: a.MaxTokens,
		Schema:    schema,
	}, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Message.Text()) != "" {
		return resp, nil
	}
	cold, err := a.Provider.Stream(ctx, llm.Request{
		Model:     a.Model,
		System:    instruction,
		Messages:  append(append([]llm.Message{}, head...), llm.TextMessage(llm.RoleUser, "Now.")),
		MaxTokens: a.MaxTokens,
	}, nil)
	if err != nil {
		return nil, err
	}
	cold.Usage.Add(resp.Usage) // both calls were paid for
	return cold, nil
}

// renderMessages flattens messages to readable text for the archive:
// role-tagged text, tool calls with their inputs, results with their ids.
// Thinking blocks are dropped; images are noted.
func renderMessages(msgs []llm.Message) string {
	var sb strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&sb, "=== message %d (%s) ===\n", i+1, m.Role)
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				sb.WriteString(strings.TrimRight(b.Text, "\n") + "\n")
			case llm.BlockToolUse:
				fmt.Fprintf(&sb, "[tool call %s %s] %s\n", b.Name, b.ID, strings.TrimSpace(string(b.Input)))
			case llm.BlockToolResult:
				tag := "result"
				if b.IsError {
					tag = "error"
				}
				fmt.Fprintf(&sb, "[tool %s %s]\n%s\n", tag, b.ToolUseID, strings.TrimRight(b.Text, "\n"))
			case llm.BlockImage:
				sb.WriteString("[image]\n")
			}
		}
	}
	return sb.String()
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

// notOverflow are errors that mention tokens or length but mean the
// request was refused for capacity, not size. They are checked first:
// Bedrock's "ThrottlingException: Too many tokens" would otherwise be read
// as an overflow, and the answer to a rate limit is to wait, not to throw
// away history.
var notOverflow = []string{
	"throttl",
	"rate limit",
	"too many requests",
	"service unavailable",
	"try again later",
	"overloaded",
}

// overflowPatterns are the ways providers say the prompt no longer fits.
// Each is a real message from a real endpoint; they disagree about almost
// every word, so the list is long and grows by observation rather than by
// guessing at what a provider ought to say.
var overflowPatterns = []string{
	"prompt is too long",                // Anthropic
	"request_too_large",                 // Anthropic
	"context_length_exceeded",           // OpenAI
	"maximum context length",            // OpenAI, OpenRouter
	"exceeds the context window",        // OpenAI
	"context window",                    // generic
	"model_context_window_exceeded",     // generic
	"input is too long",                 // generic
	"exceeds the maximum",               // Google
	"input token count",                 // Google
	"maximum prompt length",             // xAI
	"reduce the length of the messages", // Groq
	"is longer than the model",          // Together
	"available context size",            // llama.cpp
	"greater than the context length",   // LM Studio
	"context window exceeds limit",      // MiniMax
	"exceeded model token limit",        // Kimi
	"too large for model with",          // Mistral
	"range of input length should be",   // DashScope
	"exceeds the limit of",              // GitHub Copilot
}

// isContextOverflow recognises the provider errors that mean the prompt no
// longer fits the model's window.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range notOverflow {
		if strings.Contains(msg, needle) {
			return false
		}
	}
	for _, needle := range overflowPatterns {
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
func (a *Agent) trimOld(toolUse, name, subject, text string) string {
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
	id, err := a.Archive.PutSubject(toolUse, name, subject, text)
	if err != nil {
		return tool.TruncateTo(text, pruneKeep)
	}
	return archiveNote(id, len(text), pruneKeep) + tool.TruncateTo(text, pruneKeep)
}

// syncSurface records any message the harness rewrote in place — pruning
// condenses old tool results — as a replacement event, so a resumed session
// folds to exactly the history the model is being sent.
func (a *Agent) syncSurface() {
	if a.Log == nil {
		return
	}
	ev := a.Log.Events()
	seqs := session.CurrentSeqs(ev)
	if len(seqs) != len(a.Messages) {
		return // shape changed; compaction logs its own replacement
	}
	at := make(map[int]*llm.Message, len(ev))
	for i := range ev {
		at[ev[i].Seq] = ev[i].Message
	}
	for i, seq := range seqs {
		old := at[seq]
		if old == nil || sameMessage(*old, a.Messages[i]) {
			continue
		}
		m := a.Messages[i]
		t := session.TypeUserMessage
		if m.Role == llm.RoleAssistant {
			t = session.TypeAssistant
		}
		a.logEvent(session.Event{Type: t, Surface: true, SurfaceOp: session.SurfaceOpReplace, Replaces: []int{seq}, Message: &m})
	}
}

func sameMessage(x, y llm.Message) bool {
	if x.Role != y.Role || len(x.Blocks) != len(y.Blocks) {
		return false
	}
	for i := range x.Blocks {
		a, b := x.Blocks[i], y.Blocks[i]
		if a.Type != b.Type || a.Text != b.Text || a.ID != b.ID || a.Name != b.Name ||
			a.ToolUseID != b.ToolUseID || a.IsError != b.IsError || !bytes.Equal(a.Input, b.Input) {
			return false
		}
	}
	return true
}
