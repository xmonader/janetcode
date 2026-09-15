package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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

// MinCompactAt is the smallest threshold this agent can honour: the fixed
// overhead every request carries — the system prompt and the tool schemas
// — plus room for one full-size tool result. Below it the estimate is over
// the threshold before the conversation has any content at all, so the
// loop compacts after every tool call and the summary it writes is itself
// too big; nothing is ever gained and the tool result that had just
// arrived is thrown away. It must be called from the goroutine running
// the turn (it reads the system prompt and the tool registry).
func (a *Agent) MinCompactAt() int {
	return estimateTokens(a.systemPrompt()) + a.toolTokens() + InlineOutput/4
}

// raiseCompactFloor returns the threshold to use for this turn, raising a
// configured value that is below MinCompactAt rather than obeying it, and
// saying so once. The raised value is written back to CompactAt so the
// context gauge and /context agree with what actually happens.
func (a *Agent) raiseCompactFloor(emit func(Event)) int {
	at := a.CompactThreshold()
	floor := a.MinCompactAt()
	if at <= 0 || at >= floor {
		return at
	}
	if !a.compactFloorSaid {
		a.compactFloorSaid = true
		if emit != nil {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf("compact_at is ~%d tokens, below the ~%d this model's system prompt, tools and one tool result need; compacting at ~%d instead", at, floor, floor)})
		}
	}
	a.UpdateSettings(func(s *Settings) { s.CompactAt = floor })
	return floor
}

const compactSystem = `You are compacting a coding-agent conversation so work can continue in a fresh context. Write a handoff note for another engineer-agent that will only see this note. Include, with concrete file paths, commands and names:
1. The user's goal and any constraints or preferences they stated.
2. What has been done so far and what was verified (tests, builds, outputs).
3. What is in progress or still to do, in order.
4. Key facts learned about the codebase that would be expensive to rediscover.
5. Mistakes made or approaches ruled out, so they are not repeated.
Be precise and complete rather than brief. Do not add commentary; output only the note.`

// ErrNothingToCompact is returned by Compact when the history is empty.
var ErrNothingToCompact = errors.New("nothing to compact")

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

// summaryCooldown is how long compaction stops asking the model for a
// summary after a summary call failed (rate limit, outage): pruning alone
// carries the session meanwhile instead of re-hitting the provider.
const summaryCooldown = 10 * time.Minute

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

// lastAssistantStart is the index of the last assistant message, or -1.
// Unlike tailStart it ignores the size rules: it is the floor under what
// compaction keeps, not the tail it would prefer.
func lastAssistantStart(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			return i
		}
	}
	return -1
}

// toolTokens is the estimated size of the tool definitions, remembered
// between calls. Building them means asking every tool to describe
// itself and re-serialising its schema — 182µs and 434 allocations for
// seventeen tools — and the TUI asks for the estimate twice on every
// keystroke while the user types. The registry's version says when the
// answer can still be reused.
func (a *Agent) toolTokens() int {
	v := a.Tools.Version()
	if a.toolTokensAt == v && a.toolTokensFor != 0 {
		return a.toolTokensFor
	}
	n := 0
	for _, d := range a.Tools.Defs() {
		n += estimateTokens(d.Description) + estimateTokens(string(d.InputSchema))
	}
	a.toolTokensAt, a.toolTokensFor = v, n
	return n
}

// EstimateTokens approximates the prompt size of the current history. It
// uses the last real input-token count when available and a
// character-class estimate (see estimateTokens) for what was added since.
// It must be called from the goroutine running the turn.
func (a *Agent) EstimateTokens() int {
	est := estimateTokens(a.systemPrompt()) + a.toolTokens()
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

// CompactFast prunes old tool results and drops thinking from earlier
// turns. It never asks the model for a summary.
func (a *Agent) CompactFast(ctx context.Context, emit func(Event)) error {
	if emit == nil {
		emit = func(Event) {}
	}
	if len(a.Messages) == 0 {
		return ErrNothingToCompact
	}
	emit(Event{Kind: EvPhase, Text: "compacting"})
	before := a.EstimateTokens()
	saved, replaced := 0, 0
	if !a.Off["prune"] {
		saved = a.Prune()
		if limit := a.CompactThreshold(); limit > 0 {
			replaced = a.pruneTo(limit / 2)
		}
	}
	stripped := a.stripOldThinking()
	if saved > 0 || replaced > 0 || stripped > 0 {
		a.syncSurface()
	}
	after := a.EstimateTokens()
	emit(Event{Kind: EvCompacted, Text: fmt.Sprintf("context pruned: ~%d → ~%d tokens (%d old results condensed, %d thinking blocks dropped, no summary)", before, after, replaced, stripped)})
	return nil
}

// stripOldThinking removes thinking blocks from every message before the
// last assistant turn, which some providers still need as a signature.
func (a *Agent) stripOldThinking() int {
	keepFrom := lastAssistantStart(a.Messages)
	if keepFrom < 0 {
		keepFrom = len(a.Messages)
	}
	n := 0
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	for i := 0; i < keepFrom && i < len(a.Messages); i++ {
		var kept []llm.Block
		dropped := 0
		for _, b := range a.Messages[i].Blocks {
			if b.Type == llm.BlockThinking {
				dropped++
				continue
			}
			kept = append(kept, b)
		}
		if dropped == 0 {
			continue
		}
		n += dropped
		a.Messages[i].Blocks = kept
	}
	return n
}

func (a *Agent) compact(ctx context.Context, emit func(Event), force bool) error {
	if emit == nil {
		emit = func(Event) {}
	}
	if len(a.Messages) == 0 {
		return ErrNothingToCompact
	}
	emit(Event{Kind: EvPhase, Text: "compacting"})
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
	if hooks := a.HookRunner(); hooks.Has(hook.PreCompact) {
		hookNotes = hooks.Run(ctx, hook.Input{Event: hook.PreCompact, Cwd: hooks.Cwd, Tokens: before}).Notes
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
	// Keeping nothing recent is never right when there is more than one
	// exchange to compact: the summary then stands in for the turn that is
	// still running, so the tool result that had just arrived is gone and
	// the model asks for it again. Under a low threshold that repeated on
	// every tool call, each compaction reporting "0 recent messages kept".
	// Fall back to the last assistant message and everything after it.
	if ts == len(history) && len(history) > 2 {
		if k := lastAssistantStart(history); k > 0 {
			ts = k
		}
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
	note := "[Context was compacted. Handoff note from the earlier part of this conversation:]\n\n" + summary + touchedNote(head) + "\n\n" + faultDiscipline
	if typed {
		w := &a.working
		w.Latest = a.integ.userText
		w.Todos = openTodos(a.Todos())
		if !w.merge(summary) {
			emit(Event{Kind: EvNotice, Text: "working memory: the model returned prose instead of the record; kept as notes"})
		}
		if hooks := a.HookRunner(); hooks != nil {
			w.hash(hooks.Cwd)
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
	a.setMessages(append([]llm.Message{llm.HarnessMessage(llm.RoleUser, note)}, tail...))
	a.RefreshExtra() // the prefix is rebuilt anyway; pick up new memory and skills
	mergedPending := false
	if pending != nil {
		mergedPending = a.appendUserBlocks(pending.Blocks...)
	}
	a.contextManager.rewritten()
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

// touchedNote lists the files the compacted part of the conversation read
// and changed, taken from the tool calls themselves rather than from the
// summary.
//
// A summariser drops what it judges unimportant, and which files have
// already been read looks unimportant right up until the model reads them
// again. Under forced compaction that is exactly what happened: a run
// re-read the same file five times, each time having just been told a
// story in which it had never seen it, until the loop detector stopped the
// turn. Eleven of eighteen runs ended that way. A path extracted
// mechanically cannot be forgotten, because nothing had to remember it.
func touchedNote(head []llm.Message) string {
	read, changed := map[string]bool{}, map[string]bool{}
	for _, m := range head {
		for _, b := range m.Blocks {
			if b.Type != llm.BlockToolUse {
				continue
			}
			p := pathArg(b.Input)
			if p == "" {
				continue
			}
			switch b.Name {
			case "read_file":
				read[p] = true
			case "write_file", "edit_file":
				changed[p] = true
			}
		}
	}
	// A file that was changed was necessarily seen; listing it twice
	// wastes the space this note is trying to save.
	for p := range changed {
		delete(read, p)
	}
	var b strings.Builder
	if list := sortedKeys(read); len(list) > 0 {
		fmt.Fprintf(&b, "\n\nAlready read (do not read again unless you need a part you did not keep): %s", strings.Join(list, ", "))
	}
	if list := sortedKeys(changed); len(list) > 0 {
		fmt.Fprintf(&b, "\n\nAlready changed by you: %s", strings.Join(list, ", "))
	}
	return b.String()
}

// pathArg is the path a tool call names, or "".
func pathArg(input json.RawMessage) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Path)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
	s := a.Settings()
	resp, err := s.Provider.Stream(ctx, llm.Request{
		Model:     s.Model,
		System:    a.systemPrompt(),
		Messages:  msgs,
		Tools:     a.Tools.Defs(),
		MaxTokens: s.MaxTokens,
		Effort:    s.Effort,
		Schema:    schema,
	}, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Message.Text()) != "" {
		return resp, nil
	}
	cold, err := s.Provider.Stream(ctx, llm.Request{
		Model:     s.Model,
		System:    instruction,
		Messages:  append(append([]llm.Message{}, head...), llm.TextMessage(llm.RoleUser, "Now.")),
		MaxTokens: s.MaxTokens,
		Effort:    s.Effort,
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
