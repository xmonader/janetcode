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

// toolAcc accumulates a streamed tool call.
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
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var (
		text     strings.Builder
		thinking strings.Builder
		tools    = map[int]*toolAcc{}
		byID     = map[string]int{}
		order    []int
		byIndex  = map[int]int{}
		nextSlot int
		finish   string
		usage    llm.Usage
		gotDone  bool
		gotChunk bool
	)
	// Blocks are emitted in the order: thinking, text, tool calls, matching how
	// the model produced them well enough for history replay.
	for sc.Scan() {
		wd.Tick()
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
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
			return nil, fmt.Errorf("openai: bad stream chunk: %w", err)
		}
		if ch.Error != nil {
			return nil, &APIError{Status: 200, Message: ch.Error.Message}
		}
		gotChunk = true
		if ch.Usage != nil {
			pt := ch.Usage.PromptTokens
			cached := 0
			if ch.Usage.PromptTokensDetails != nil {
				cached = ch.Usage.PromptTokensDetails.CachedTokens
			}
			// OpenAI/DashScope: cached_tokens is a subset of prompt_tokens.
			// Store uncached input so Cost() and CacheRate() stay additive
			// like the Anthropic path (input_tokens is already uncached there).
			usage.OutputTokens = ch.Usage.CompletionTokens
			usage.CacheReadTokens = cached
			if cached > 0 && cached <= pt {
				usage.InputTokens = pt - cached
			} else {
				usage.InputTokens = pt
			}
			usage.CostUSD = ch.Usage.Cost
		}
		for _, c := range ch.Choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
			d := c.Delta
			if d.ReasoningContent != "" || d.Reasoning != "" {
				rc := d.ReasoningContent + d.Reasoning
				thinking.WriteString(rc)
				emit(llm.Event{Type: llm.EventThinkingDelta, Text: rc})
			}
			if d.Content != "" {
				text.WriteString(d.Content)
				emit(llm.Event{Type: llm.EventTextDelta, Text: d.Content})
			}
			for i, tc := range d.ToolCalls {
				idx := slotFor(tc.Index, i, tc.ID, tc.Function.Name, tools, order, byIndex, byID, &nextSlot)
				acc := tools[idx]
				if !slices.Contains(order, idx) {
					order = append(order, idx)
				}
				if tc.ID != "" {
					acc.id = tc.ID
					if _, seen := byID[tc.ID]; !seen {
						byID[tc.ID] = idx
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
					emit(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{Type: llm.BlockToolUse, ID: acc.id, Name: acc.name}})
				}
				if tc.Function.Arguments != "" {
					delta := mergeArguments(&acc.args, tc.Function.Arguments)
					if delta != "" {
						emit(llm.Event{Type: llm.EventToolUseDelta, Text: delta})
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("openai: stream read: %w", err)
	}
	if !gotChunk {
		return nil, errors.New("openai: empty stream from server")
	}
	if !gotDone && finish == "" {
		return nil, errors.New("openai: stream ended without finish_reason")
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	if thinking.Len() > 0 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: thinking.String()})
	}
	if text.Len() > 0 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: text.String()})
	}
	// Stable, so calls that shared a wire index keep the order they were
	// streamed in.
	sort.SliceStable(order, func(i, j int) bool { return tools[order[i]].sortKey < tools[order[j]].sortKey })
	for _, idx := range order {
		acc := tools[idx]
		args := strings.TrimSpace(acc.args.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return nil, fmt.Errorf("openai: tool call %s has invalid JSON arguments: %.200s", acc.name, args)
		}
		b := llm.Block{Type: llm.BlockToolUse, ID: acc.id, Name: acc.name, Input: json.RawMessage(args)}
		if !acc.started {
			emit(llm.Event{Type: llm.EventToolUseStart, Block: &b})
		}
		emit(llm.Event{Type: llm.EventToolUseEnd, Block: &b})
		msg.Blocks = append(msg.Blocks, b)
	}
	if len(msg.Blocks) == 0 {
		msg.Blocks = []llm.Block{{Type: llm.BlockText, Text: ""}}
	}
	stop := llm.StopEndTurn
	switch {
	case len(order) > 0:
		stop = llm.StopToolUse
	case finish == "length":
		stop = llm.StopMaxTokens
	case finish == "content_filter":
		stop = llm.StopRefusal
	case finish == "stop", finish == "":
		stop = llm.StopEndTurn
	default:
		stop = llm.StopOther
	}
	return &llm.Response{Message: msg, StopReason: stop, Usage: usage}, nil
}
