// Package llm defines the provider-neutral model of a chat conversation with
// tool use, plus the Provider interface every backend implements.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Role is the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType discriminates the content blocks in a message.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is one unit of content in a message. Which fields are meaningful
// depends on Type.
type Block struct {
	Type BlockType `json:"type"`

	// Text holds text for BlockText, the reasoning for BlockThinking, and the
	// result payload for BlockToolResult.
	Text string `json:"text,omitempty"`

	// Tool call fields (BlockToolUse).
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// Tool result fields (BlockToolResult).
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Signature is opaque provider data attached to thinking blocks that must
	// be echoed back verbatim on later turns.
	Signature string `json:"signature,omitempty"`
}

// Message is one turn in the conversation.
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

// TextMessage builds a single-text-block message.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Blocks: []Block{{Type: BlockText, Text: text}}}
}

// ToolResultMessage wraps tool results as a user turn. All results for one
// assistant turn must go in a single message.
func ToolResultMessage(results ...Block) Message {
	return Message{Role: RoleUser, Blocks: results}
}

// ToolResult builds a tool_result block.
func ToolResult(toolUseID, content string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolUseID: toolUseID, Text: content, IsError: isError}
}

// Text concatenates the message's text blocks.
func (m Message) Text() string {
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Type == BlockText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// ToolUses returns the tool_use blocks in order.
func (m Message) ToolUses() []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Request is one model call.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// Usage is token accounting for one call.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	// CostUSD is the price the provider reported for the request (OpenRouter
	// does), or 0 when unknown.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CacheReadTokens += u2.CacheReadTokens
	u.CacheWriteTokens += u2.CacheWriteTokens
	u.CostUSD += u2.CostUSD
}

// Price is what a model costs per million tokens, in USD.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Cost returns the provider-reported cost when there is one, otherwise an
// estimate from the price table; ok is false when neither is available.
func (u Usage) Cost(p *Price) (usd float64, ok bool) {
	if u.CostUSD > 0 {
		return u.CostUSD, true
	}
	if p == nil {
		return 0, false
	}
	m := 1e6
	usd = float64(u.InputTokens)/m*p.Input + float64(u.OutputTokens)/m*p.Output +
		float64(u.CacheReadTokens)/m*p.CacheRead + float64(u.CacheWriteTokens)/m*p.CacheWrite
	return usd, true
}

// FormatUSD renders a cost compactly: $0.0042, $0.12, $3.40.
func FormatUSD(usd float64) string {
	switch {
	case usd < 0.01:
		return fmt.Sprintf("$%.4f", usd)
	case usd < 1:
		return fmt.Sprintf("$%.3f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// StopReason says why the model stopped generating.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

// Response is the fully assembled result of a call.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
}

// EventType identifies a streaming event.
type EventType string

const (
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventToolUseStart  EventType = "tool_use_start"
	EventToolUseDelta  EventType = "tool_use_delta"
	EventToolUseEnd    EventType = "tool_use_end"
)

// Event is one streamed increment. Text carries the delta for text, thinking
// and partial tool-input JSON. Block is set on tool_use start (ID+Name) and
// end (complete Input).
type Event struct {
	Type  EventType
	Text  string
	Block *Block
}

// Provider is a chat backend. Stream must call emit for every increment on the
// calling goroutine, honour ctx cancellation, and return the assembled
// response. emit may be nil.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error)
}

// ErrNoAPIKey is returned when a provider is constructed without credentials.
var ErrNoAPIKey = errors.New("llm: no API key configured")

// Validate rejects requests that no provider can serve.
func (r Request) Validate() error {
	if r.Model == "" {
		return errors.New("llm: request has no model")
	}
	if len(r.Messages) == 0 {
		return errors.New("llm: request has no messages")
	}
	if r.Messages[0].Role != RoleUser {
		return errors.New("llm: first message must be from user")
	}
	for i, m := range r.Messages {
		if len(m.Blocks) == 0 {
			return fmt.Errorf("llm: empty message at index %d", i)
		}
	}
	return nil
}
