package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// slotFor decides which tool call a streamed delta belongs to.
//
// Providers disagree about how they number tool calls, and every way of
// getting this wrong concatenates two calls into one — a tool named
// "read_fileedit_file" whose arguments are two JSON objects glued
// together, which ends the turn. Three behaviours have to be handled:
//
//   - the index is present and distinct, the well-behaved case;
//   - the index is absent, and then a delta carrying an id or a function
//     name begins a call while one carrying only argument text continues
//     the call in progress (several deltas in one chunk are distinct);
//   - the index is present but reused for a second, different call, which
//     a new id gives away.
//
// Slots are therefore not indices. Each call gets its own monotonic slot,
// and the wire index is kept only as the sort key, so sparse or
// out-of-order indices still order correctly while a reused one does not
// collide.
//
// An id, when there is one, outranks all of that. A provider that sends
// several index-less calls per chunk and then continues them — chunk one
// opens a and b, chunk two carries arguments for a and then for b — has
// nothing but the id to say which is which, and the position heuristic
// reads every continuation as a new call. So an index-less delta whose id
// is already accumulating goes back to its own slot, and only a delta with
// no id at all falls through to the heuristic.
func slotFor(index *int, pos int, id, name string, tools map[int]*toolAcc, order []int, byIndex map[int]int, byID map[string]int, next *int) int {
	newSlot := func(sortKey int) int {
		slot := *next
		*next++
		tools[slot] = &toolAcc{sortKey: sortKey}
		return slot
	}
	if index != nil {
		slot, seen := byIndex[*index]
		if seen {
			// A different id on a claimed index means the provider has
			// moved on to another call and reused the number.
			if acc := tools[slot]; id == "" || acc.id == "" || id == acc.id {
				return slot
			}
		}
		slot = newSlot(*index)
		byIndex[*index] = slot
		return slot
	}
	if id != "" {
		if slot, seen := byID[id]; seen {
			return slot
		}
	}
	if len(order) == 0 {
		return newSlot(0)
	}
	cur := order[len(order)-1]
	acc := tools[cur]
	fresh := pos > 0 ||
		(id != "" && acc.id != "" && id != acc.id) ||
		(name != "" && acc.name != "" && (acc.args.Len() > 0 || id != ""))
	if !fresh {
		return cur
	}
	return newSlot(*next)
}

// mergeArguments folds one streamed argument chunk into what has been
// accumulated, and reports the text that was actually added.
//
// Some providers do not stream true deltas: they resend the whole argument
// string every chunk. Appending those turns {"path": into
// {"path":{"path":"a.go"} and the turn dies on invalid JSON. A resend is
// recognisable without guessing, because it repeats what is already held —
// so a chunk that starts with everything accumulated so far replaces it,
// and anything else is appended. That distinguishes a resend from a
// genuine fragment that merely happens to begin with a brace.
func mergeArguments(buf *strings.Builder, chunk string) string {
	cur := buf.String()
	if cur == "" || !strings.HasPrefix(chunk, cur) {
		buf.WriteString(chunk)
		return chunk
	}
	added := chunk[len(cur):]
	buf.Reset()
	buf.WriteString(chunk)
	return added
}

// mergeName folds one streamed function-name delta into the name held so
// far, and is the name side of what mergeArguments does for arguments.
//
// Some providers repeat the whole function name on every delta of a call
// rather than sending it once. Appending those gives "read_fileread_file",
// a tool nothing is registered under, and the turn ends on an unknown
// tool. A repeat is recognisable the same way a resent argument string is:
// it is what is already held, or it grows it from the front. Anything else
// is a genuine fragment ("read_" then "file") and is appended.
func mergeName(cur, chunk string) string {
	switch {
	case cur == "":
		return chunk
	case chunk == "" || chunk == cur:
		return cur
	case strings.HasPrefix(chunk, cur):
		// The name so far, resent with more of it: take the longer one.
		return chunk
	case strings.HasSuffix(cur, chunk):
		// The tail again. A real fragment that repeats the end of the
		// name it is extending would have to be a tool called something
		// like "aa" streamed one letter at a time; a resend is far more
		// likely, and doubling the name is fatal where dropping a
		// duplicate is not.
		return cur
	}
	return cur + chunk
}

// toolAcc accumulates a streamed tool call.
type toolAcc struct {
	id, name string
	args     strings.Builder
	started  bool
	// sortKey is the wire index when the provider sent one, so calls are
	// ordered as the model meant them rather than as they arrived.
	sortKey int
}

// readStream assembles the SSE body. wd, when set, is told about every
// frame so a stream that keeps talking is never cut off for being idle.
func readStream(r io.Reader, wd *llm.Watchdog, emit func(llm.Event)) (*llm.Response, error) {
	sc := bufio.NewScanner(r)
	// The scanner grows its buffer as a line needs it, so start small —
	// all but a handful of frames fit in a few kilobytes — and keep the
	// cap high enough for one enormous tool-call frame.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	st := newStreamState(emit)
	var gotDone, gotChunk bool
	// What a server that is not speaking SSE at all sent instead, kept so
	// the failure names it rather than saying the stream was empty: a
	// plain JSON body, an HTML error page from a proxy, a login redirect.
	var preamble strings.Builder
	// Blocks are emitted in the order: thinking, text, tool calls, matching how
	// the model produced them well enough for history replay.
	for sc.Scan() {
		wd.Tick()
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			// ":" opens an SSE comment, the usual keep-alive; anything
			// else non-blank is a field we do not use, or not SSE at all.
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, ":") && preamble.Len() < 400 {
				preamble.WriteString(t)
				preamble.WriteByte('\n')
			}
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			gotDone = true
			break
		}
		if data == "" {
			// Gateways send an empty data frame as a keep-alive. Handing
			// it to the JSON decoder ends the turn with "unexpected end
			// of JSON input" halfway through a working stream.
			continue
		}
		var ch wireChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return nil, llm.Truncate(fmt.Errorf("openai: bad stream chunk: %w", err))
		}
		if ch.Error != nil {
			// The server spoke: a structured refusal is not a truncation.
			return nil, streamError(ch.Error)
		}
		gotChunk = true
		st.add(ch)
	}
	if err := sc.Err(); err != nil {
		return nil, llm.Truncate(fmt.Errorf("openai: stream read: %w", err))
	}
	if !gotChunk {
		if body := strings.TrimSpace(preamble.String()); body != "" {
			// BoundErrorMessage, not %.300s: that cut bytes, so it could
			// split a UTF-8 sequence, and it redacted nothing — a 200
			// from a proxy that echoed the request headers put the key
			// straight into the transcript.
			return nil, llm.Truncate(fmt.Errorf("openai: no stream frames from server: %s", llm.BoundErrorMessage(body)))
		}
		return nil, llm.Truncate(errors.New("openai: empty stream from server"))
	}
	if !gotDone && st.finish == "" {
		return nil, llm.Truncate(errors.New("openai: stream ended without finish_reason"))
	}
	return st.response()
}

// streamState is what readStream has accumulated from the chunks so far.
type streamState struct {
	emit     func(llm.Event)
	text     strings.Builder
	thinking strings.Builder
	inline   llm.ThinkSplitter
	tools    map[int]*toolAcc
	byID     map[string]int
	order    []int
	byIndex  map[int]int
	nextSlot int
	finish   string
	usage    llm.Usage
	model    string
	// served is what the chunks said about who served the call; its
	// ToolIDs is the format of the first id the server minted (an id
	// janetcode had to invent for a call that came without one says
	// nothing about the server).
	served llm.Provenance
}

func newStreamState(emit func(llm.Event)) *streamState {
	return &streamState{emit: emit, tools: map[int]*toolAcc{}, byID: map[string]int{}, byIndex: map[int]int{}}
}

// add folds one decoded chunk into the state, emitting deltas as they
// arrive.
func (s *streamState) add(ch wireChunk) {
	if ch.Model != "" {
		s.model = ch.Model
	}
	if s.served.IDPrefix == "" && ch.ID != "" {
		s.served.IDPrefix = llm.IDPrefix(ch.ID)
	}
	if ch.Provider != "" {
		s.served.Provider = ch.Provider
	}
	if b := ch.XAIBroker; b != nil {
		if b.Provider != "" {
			s.served.Provider = b.Provider
		}
		if b.ProviderModel != "" {
			s.served.ProviderModel = b.ProviderModel
		}
	}
	if ch.Usage != nil {
		s.usage = usageOf(ch.Usage)
	}
	for _, c := range ch.Choices {
		if c.FinishReason != nil && *c.FinishReason != "" {
			s.finish = *c.FinishReason
		}
		d := c.Delta
		// The same reasoning can arrive under several names in one delta
		// (OpenRouter fills "reasoning" and "reasoning_details" with the
		// same text), so the first one present is the delta and the rest
		// are copies of it.
		rc := d.ReasoningContent
		if rc == "" {
			rc = d.Reasoning
		}
		if rc != "" {
			s.thinking.WriteString(rc)
			s.emit(llm.Event{Type: llm.EventThinkingDelta, Text: rc})
		}
		for _, p := range d.ReasoningDetails {
			if rc != "" {
				break
			}
			part := p.Text
			if part == "" {
				part = p.Summary
			}
			if part == "" {
				continue
			}
			s.thinking.WriteString(part)
			s.emit(llm.Event{Type: llm.EventThinkingDelta, Text: part})
		}
		if d.Content.Thinking != "" {
			s.addContent("", d.Content.Thinking)
		}
		if d.Content.Text != "" {
			s.addContent(s.inline.Feed(d.Content.Text))
		}
		for i, tc := range d.ToolCalls {
			s.addToolCall(i, tc)
		}
	}
}

// addContent records reply text and the reasoning a model wrote inline in
// it, each as its own delta.
func (s *streamState) addContent(text, think string) {
	if think != "" {
		s.thinking.WriteString(think)
		s.emit(llm.Event{Type: llm.EventThinkingDelta, Text: think})
	}
	if text != "" {
		s.text.WriteString(text)
		s.emit(llm.Event{Type: llm.EventTextDelta, Text: text})
	}
}

// usageOf converts the wire usage.
func usageOf(u *wireUsage) llm.Usage {
	pt := int(u.PromptTokens)
	cached := 0
	if u.PromptTokensDetails != nil {
		cached = int(u.PromptTokensDetails.CachedTokens)
	}
	// OpenAI/DashScope: cached_tokens is a subset of prompt_tokens.
	// Store uncached input so Cost() and CacheRate() stay additive
	// like the Anthropic path (input_tokens is already uncached there).
	usage := llm.Usage{OutputTokens: int(u.CompletionTokens), CacheReadTokens: cached, CostUSD: float64(u.Cost)}
	if u.CompletionTokensDetails != nil {
		usage.ReasoningTokens = int(u.CompletionTokensDetails.ReasoningTokens)
	}
	if cached > 0 && cached <= pt {
		usage.InputTokens = pt - cached
	} else {
		usage.InputTokens = pt
	}
	return usage
}

// addToolCall folds the pos-th tool-call delta of a chunk into its call.
func (s *streamState) addToolCall(pos int, tc wireDeltaToolCall) {
	idx := slotFor(tc.Index, pos, tc.ID, tc.Function.Name, s.tools, s.order, s.byIndex, s.byID, &s.nextSlot)
	acc := s.tools[idx]
	if !slices.Contains(s.order, idx) {
		s.order = append(s.order, idx)
	}
	if tc.ID != "" {
		if s.served.ToolIDs == "" {
			s.served.ToolIDs = llm.ToolIDFormat(tc.ID)
		}
		acc.id = tc.ID
		if _, seen := s.byID[tc.ID]; !seen {
			s.byID[tc.ID] = idx
		}
	}
	if tc.Function.Name != "" {
		acc.name = mergeName(acc.name, tc.Function.Name)
	}
	if !acc.started && acc.name != "" {
		acc.started = true
		if acc.id == "" {
			acc.id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), idx)
		}
		s.emit(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{Type: llm.BlockToolUse, ID: acc.id, Name: acc.name}})
	}
	if tc.Function.Arguments != "" {
		delta := mergeArguments(&acc.args, tc.Function.Arguments)
		if delta != "" {
			s.emit(llm.Event{Type: llm.EventToolUseDelta, Text: delta})
		}
	}
}

// response builds the assistant message from a completed stream.
func (s *streamState) response() (*llm.Response, error) {
	s.addContent(s.inline.Flush())
	msg := llm.Message{Role: llm.RoleAssistant}
	calls := 0
	if s.thinking.Len() > 0 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: s.thinking.String()})
	}
	if s.text.Len() > 0 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: s.text.String()})
	}
	// Stable, so calls that shared a wire index keep the order they were
	// streamed in.
	sort.SliceStable(s.order, func(i, j int) bool { return s.tools[s.order[i]].sortKey < s.tools[s.order[j]].sortKey })
	for _, idx := range s.order {
		acc := s.tools[idx]
		args := strings.TrimSpace(acc.args.String())
		// A call with no function name is not a call: nothing can be run,
		// and put back in the history as a tool_use with an empty name it
		// makes every later turn invalid for the provider. A slot that
		// accumulated nothing at all is a delta that carried no call and
		// is dropped; one that carried an id or arguments is reported,
		// because dropping it would silently lose what the model asked
		// for and leave the turn looking like it just stopped.
		if acc.name == "" {
			if args == "" && acc.id == "" {
				continue
			}
			return nil, fmt.Errorf("openai: tool call %q has no function name: %.200s", acc.id, args)
		}
		if args == "" {
			args = "{}"
		}
		// Arguments that are not JSON cost the whole turn if they fail the
		// stream, and the response is already paid for. A structural slip
		// is mended; anything else goes to the agent wrapped, and the
		// model is told to resend the call. A response cut off at the
		// output limit is never mended: its calls are refused whole.
		if !json.Valid([]byte(args)) {
			if fixed, ok := llm.RepairJSON(args); ok && s.finish != "length" {
				args = fixed
			} else {
				args = string(llm.InvalidArguments(args))
			}
		}
		// acc.started is always true here: addToolCall starts a call as
		// soon as it has a name, which is what got it past the guard above.
		b := llm.Block{Type: llm.BlockToolUse, ID: acc.id, Name: acc.name, Input: json.RawMessage(args)}
		s.emit(llm.Event{Type: llm.EventToolUseEnd, Block: &b})
		msg.Blocks = append(msg.Blocks, b)
		calls++
	}
	if len(msg.Blocks) == 0 {
		msg.Blocks = []llm.Block{{Type: llm.BlockText, Text: ""}}
	}
	served := s.served
	served.Model = s.model
	return &llm.Response{Message: msg, StopReason: stopReason(s.finish, calls > 0), Usage: s.usage, Model: s.model, Served: served}, nil
}

// stopReason maps finish_reason; a response that called tools stops for
// them whatever the server said.
func stopReason(finish string, calledTools bool) llm.StopReason {
	switch {
	case finish == "length":
		// Before calledTools: calls cut off at the output limit may have
		// lost part of their arguments, and the agent refuses them.
		return llm.StopMaxTokens
	case calledTools:
		return llm.StopToolUse
	case finish == "content_filter":
		return llm.StopRefusal
	case finish == "stop", finish == "":
		return llm.StopEndTurn
	default:
		return llm.StopOther
	}
}
