package responses

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/httpstream"
)

// event is the union of the stream events this provider reads.
type event struct {
	Type        string          `json:"type"`
	OutputIndex *int            `json:"output_index"`
	ItemID      string          `json:"item_id"`
	Delta       string          `json:"delta"`
	Item        json.RawMessage `json:"item"`
	Response    *struct {
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *apiError `json:"error"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
	// An "error" event carries these at the top level.
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type apiError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type item struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Summary          []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

// slot is one output item being assembled.
type slot struct {
	block  llm.Block
	args   strings.Builder
	done   bool
	refuse bool
}

type stream struct {
	emit  func(llm.Event)
	slots map[int]*slot
	byID  map[string]int
	next  int // index for frames that name no output_index
	usage llm.Usage
	model string
	stop  llm.StopReason
}

func newStream(emit func(llm.Event)) *stream {
	return &stream{emit: emit, slots: map[int]*slot{}, byID: map[string]int{}}
}

func (s *stream) index(e event) int {
	if e.OutputIndex != nil {
		return *e.OutputIndex
	}
	if i, ok := s.byID[e.ItemID]; ok {
		return i
	}
	return s.next
}

func (s *stream) slot(i int, t llm.BlockType) *slot {
	sl, ok := s.slots[i]
	if !ok {
		sl = &slot{block: llm.Block{Type: t}}
		s.slots[i] = sl
		if i >= s.next {
			s.next = i + 1
		}
	}
	return sl
}

// add reads one frame and reports whether it ended the response.
func (s *stream) add(name string, data []byte) (bool, error) {
	var e event
	if err := json.Unmarshal(data, &e); err != nil {
		return false, llm.Truncate(fmt.Errorf("responses: bad stream frame: %w", err))
	}
	if e.Type == "" {
		e.Type = name
	}
	switch e.Type {
	case "response.output_item.added":
		var it item
		if json.Unmarshal(e.Item, &it) != nil {
			return false, nil
		}
		i := s.index(e)
		if it.ID != "" {
			s.byID[it.ID] = i
		}
		switch it.Type {
		case "function_call":
			sl := s.slot(i, llm.BlockToolUse)
			sl.block.ID, sl.block.Name = callID(it), it.Name
			start := sl.block
			s.emit(llm.Event{Type: llm.EventToolUseStart, Block: &start})
		case "reasoning":
			s.slot(i, llm.BlockThinking).block.ID = it.ID
		case "message":
			s.slot(i, llm.BlockText)
		}
	case "response.output_text.delta", "response.refusal.delta":
		if e.Delta == "" {
			break
		}
		sl := s.slot(s.index(e), llm.BlockText)
		sl.block.Text += e.Delta
		if e.Type == "response.refusal.delta" {
			sl.refuse = true
		}
		s.emit(llm.Event{Type: llm.EventTextDelta, Text: e.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if e.Delta == "" {
			break
		}
		sl := s.slot(s.index(e), llm.BlockThinking)
		sl.block.Text += e.Delta
		s.emit(llm.Event{Type: llm.EventThinkingDelta, Text: e.Delta})
	case "response.reasoning_summary_part.done":
		// Summary parts of one item run together otherwise.
		if sl, ok := s.slots[s.index(e)]; ok && sl.block.Type == llm.BlockThinking && !strings.HasSuffix(sl.block.Text, "\n\n") {
			sl.block.Text += "\n\n"
		}
	case "response.function_call_arguments.delta":
		sl := s.slot(s.index(e), llm.BlockToolUse)
		sl.args.WriteString(e.Delta)
		s.emit(llm.Event{Type: llm.EventToolUseDelta, Text: e.Delta})
	case "response.output_item.done":
		var it item
		if json.Unmarshal(e.Item, &it) != nil {
			return false, nil
		}
		s.finishItem(s.index(e), it)
	case "response.completed", "response.incomplete":
		if r := e.Response; r != nil {
			s.model = r.Model
			if u := r.Usage; u != nil {
				cached, reasoning := 0, 0
				if u.InputTokensDetails != nil {
					cached = u.InputTokensDetails.CachedTokens
				}
				if u.OutputTokensDetails != nil {
					reasoning = u.OutputTokensDetails.ReasoningTokens
				}
				s.usage = llm.Usage{InputTokens: max(u.InputTokens-cached, 0), OutputTokens: u.OutputTokens, CacheReadTokens: cached, ReasoningTokens: reasoning}
			}
			if d := r.IncompleteDetails; d != nil {
				switch d.Reason {
				case "max_output_tokens":
					s.stop = llm.StopMaxTokens
				case "content_filter":
					s.stop = llm.StopRefusal
				default:
					s.stop = llm.StopOther
				}
			}
		}
		return true, nil
	case "response.failed":
		var ae *apiError
		if e.Response != nil {
			ae = e.Response.Error
		}
		if ae == nil {
			ae = &apiError{Message: "the response failed"}
		}
		return false, streamError(ae)
	case "error":
		return false, streamError(&apiError{Code: e.Code, Message: e.Message})
	}
	return false, nil
}

// finishItem settles one output item from its final form, which is
// authoritative over the deltas that built it.
func (s *stream) finishItem(i int, it item) {
	switch it.Type {
	case "function_call":
		sl := s.slot(i, llm.BlockToolUse)
		sl.block.Type = llm.BlockToolUse
		sl.block.ID, sl.block.Name = callID(it), it.Name
		args := it.Arguments
		if args == "" {
			args = sl.args.String()
		}
		sl.block.Input = arguments(args, true)
		if !sl.done {
			sl.done = true
			end := sl.block
			s.emit(llm.Event{Type: llm.EventToolUseEnd, Block: &end})
		}
	case "reasoning":
		sl := s.slot(i, llm.BlockThinking)
		sl.block.ID = it.ID
		sl.block.Signature = it.EncryptedContent
		if strings.TrimSpace(sl.block.Text) == "" {
			var parts []string
			for _, p := range it.Summary {
				parts = append(parts, p.Text)
			}
			sl.block.Text = strings.Join(parts, "\n\n")
		}
		sl.block.Text = strings.TrimRight(sl.block.Text, "\n")
	case "message":
		sl := s.slot(i, llm.BlockText)
		if sl.block.Text == "" {
			for _, c := range it.Content {
				sl.block.Text += c.Text + c.Refusal
				if c.Type == "refusal" {
					sl.refuse = true
				}
			}
		}
	}
}

// arguments turns a call's argument text into its input. Text that is
// not JSON cannot go into the block as it is: the session log and the
// next request both marshal it, and encoding/json refuses an invalid raw
// message. A structural slip in a call that closed is mended; anything
// else is wrapped, so the model is told to resend the call (as the chat
// completions path does).
func arguments(args string, closed bool) json.RawMessage {
	if strings.TrimSpace(args) == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}
	if fixed, ok := llm.RepairJSON(args); ok && closed {
		return json.RawMessage(fixed)
	}
	return llm.InvalidArguments(args)
}

func callID(it item) string {
	if it.CallID != "" {
		return it.CallID
	}
	return it.ID
}

func (s *stream) response() *llm.Response {
	idx := make([]int, 0, len(s.slots))
	for i := range s.slots {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	msg := llm.Message{Role: llm.RoleAssistant}
	calls, refused := false, false
	for _, i := range idx {
		sl := s.slots[i]
		b := sl.block
		switch b.Type {
		case llm.BlockToolUse:
			if len(b.Input) == 0 {
				// Arguments streamed but the item never closed: the reply
				// was cut off, so they are never mended.
				b.Input = arguments(sl.args.String(), false)
			}
			calls = true
		case llm.BlockText:
			if b.Text == "" {
				continue
			}
			refused = refused || sl.refuse
		case llm.BlockThinking:
			if b.Text == "" && b.Signature == "" {
				continue
			}
		}
		msg.Blocks = append(msg.Blocks, b)
	}
	stop := s.stop
	switch {
	case stop != "":
	case calls:
		stop = llm.StopToolUse
	case refused:
		stop = llm.StopRefusal
	default:
		stop = llm.StopEndTurn
	}
	return &llm.Response{Message: msg, StopReason: stop, Usage: s.usage, Model: s.model}
}

// streamError turns an error the server reported mid-stream into one the
// retry policy can judge: a rate limit or an overload is worth another
// attempt, anything else is final.
func streamError(e *apiError) error {
	code := strings.ToLower(fmt.Sprint(e.Code))
	msg := llm.BoundErrorMessage(e.Message)
	if msg == "" {
		msg = code
	}
	status := 200
	switch {
	case strings.Contains(code, "rate_limit"):
		status = 429
	case strings.Contains(code, "server_error") || strings.Contains(code, "overloaded"):
		status = 503
	}
	return &httpstream.APIError{Status: status, Message: msg, Detail: llm.Redact(e.Message)}
}
