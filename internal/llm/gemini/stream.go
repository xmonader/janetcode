package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

type chunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             string `json:"text"`
				Thought          bool   `json:"thought"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
	Error        *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// stream accumulates the frames into blocks in the order the model made
// them: thinking, then text and calls as they come.
type stream struct {
	emit     func(llm.Event)
	frames   int
	blocks   []llm.Block
	finish   string
	blocked  string
	finMsg   string
	usage    llm.Usage
	model    string
	calls    int
	pending  string // a signature that came on a part with nothing to carry it
	hasCalls bool
}

// last returns the open block of type t at the end, or appends one.
func (s *stream) last(t llm.BlockType) *llm.Block {
	if n := len(s.blocks); n > 0 && s.blocks[n-1].Type == t {
		return &s.blocks[n-1]
	}
	s.blocks = append(s.blocks, llm.Block{Type: t})
	return &s.blocks[len(s.blocks)-1]
}

func (s *stream) add(data []byte) error {
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return llm.Truncate(fmt.Errorf("gemini: bad stream frame: %w", err))
	}
	if c.Error != nil {
		return fmt.Errorf("gemini: %s (code %d)", llm.BoundErrorMessage(c.Error.Message), c.Error.Code)
	}
	s.frames++
	if c.ModelVersion != "" {
		s.model = c.ModelVersion
	}
	if u := c.UsageMetadata; u != nil {
		// Each frame carries the running total; the last one is the call's.
		s.usage = llm.Usage{
			InputTokens:     max(u.PromptTokenCount-u.CachedContentTokenCount, 0),
			OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
			CacheReadTokens: u.CachedContentTokenCount,
			ReasoningTokens: u.ThoughtsTokenCount,
		}
	}
	if pf := c.PromptFeedback; pf != nil && pf.BlockReason != "" {
		s.blocked, s.finish = pf.BlockReason, "BLOCKED"
	}
	if len(c.Candidates) > 0 { // one candidate is asked for
		cand := c.Candidates[0]
		for _, p := range cand.Content.Parts {
			s.part(p.Text, p.Thought, p.ThoughtSignature, p.FunctionCall)
		}
		if cand.FinishReason != "" {
			s.finish = cand.FinishReason
			s.finMsg = cand.FinishMessage
		}
	}
	return nil
}

func (s *stream) part(text string, thought bool, sig string, fc *struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}) {
	if sig == "" {
		sig, s.pending = s.pending, ""
	}
	switch {
	case fc != nil:
		s.calls++
		s.hasCalls = true
		id := fc.ID
		if id == "" {
			id = fmt.Sprintf("%s%d", callPrefix, s.calls)
		}
		args := fc.Args
		if len(strings.TrimSpace(string(args))) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		b := llm.Block{Type: llm.BlockToolUse, ID: id, Name: fc.Name, Input: args, Signature: sig}
		s.blocks = append(s.blocks, b)
		start := llm.Block{Type: llm.BlockToolUse, ID: id, Name: fc.Name}
		s.emit(llm.Event{Type: llm.EventToolUseStart, Block: &start})
		s.emit(llm.Event{Type: llm.EventToolUseDelta, Text: string(args)})
		end := b
		s.emit(llm.Event{Type: llm.EventToolUseEnd, Block: &end})
	case thought:
		if text != "" {
			s.last(llm.BlockThinking).Text += text
			s.emit(llm.Event{Type: llm.EventThinkingDelta, Text: text})
		}
		// A signature on a thought belongs to what the thought led to.
		s.pending = sig
	case text != "":
		b := s.last(llm.BlockText)
		b.Text += text
		if sig != "" {
			b.Signature = sig
		}
		s.emit(llm.Event{Type: llm.EventTextDelta, Text: text})
	default:
		s.pending = sig // an empty part that only carries a signature
	}
}

func (s *stream) response() *llm.Response {
	if s.pending != "" {
		for i := len(s.blocks) - 1; i >= 0; i-- {
			if b := &s.blocks[i]; b.Type == llm.BlockText && b.Signature == "" {
				b.Signature = s.pending
				break
			}
		}
	}
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: s.blocks}
	stop := llm.StopEndTurn
	switch s.finish {
	case "MAX_TOKENS":
		stop = llm.StopMaxTokens
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "BLOCKED":
		stop = llm.StopRefusal
	case "STOP", "":
	default:
		stop = llm.StopOther
	}
	if s.hasCalls && stop == llm.StopEndTurn {
		stop = llm.StopToolUse
	}
	if stop == llm.StopRefusal && len(msg.ToolUses()) == 0 && strings.TrimSpace(msg.Text()) == "" {
		why := s.blocked
		if why == "" {
			why = s.finish
		}
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: "(Gemini declined to answer: " + strings.ToLower(why) + ")"})
	}
	if stop == llm.StopOther && s.finMsg != "" && strings.TrimSpace(msg.Text()) == "" {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: "(Gemini stopped: " + llm.BoundErrorMessage(s.finMsg) + ")"})
	}
	return &llm.Response{Message: msg, StopReason: stop, Usage: s.usage, Model: s.model}
}
