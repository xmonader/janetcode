// Package llm defines the provider-neutral model of a chat conversation with
// tool use, plus the Provider interface every backend implements.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/textfmt"
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
	// BlockImage carries a base64 image in a user message.
	BlockImage BlockType = "image"
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

	// ReplyID is the id the provider itself gave a tool call, when the
	// harness had to rename it (a provider that repeats or omits ids).
	// The reply goes back under this one: it is the only id the provider
	// knows, while ID is what the harness keys by.
	ReplyID string `json:"reply_id,omitempty"`

	// Tool result fields (BlockToolResult).
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Synthetic marks text the harness itself wrote into the user role —
	// a verdict correction, a failing check's output, a background job
	// update, a compaction note — as opposed to what the user typed.
	// Providers send it like any text. Anything that reads the transcript
	// for what the user wants (memory extraction) must not take it as the
	// user's words.
	Synthetic bool `json:"synthetic,omitempty"`

	// Image fields (BlockImage): MediaType like "image/png", Data base64.
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`

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

// HarnessMessage is TextMessage for text the harness wrote, marked so.
func HarnessMessage(role Role, text string) Message {
	return Message{Role: role, Blocks: []Block{{Type: BlockText, Text: text, Synthetic: true}}}
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

// WireID is the id a block goes out to the provider under: the
// provider's own, when the harness renamed the call.
func (b Block) WireID() string {
	if b.ReplyID != "" {
		return b.ReplyID
	}
	return b.ID
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
	// Effort asks for a reasoning tier from EffortTiers for this one
	// call; empty leaves the provider's default (or, where the provider
	// was configured with one, that). A provider whose request shape has
	// no such field ignores it, so setting it is always safe.
	Effort string
	// Schema, when set, constrains the reply to JSON matching it. A
	// provider that cannot do this ignores it and the caller parses as
	// before, so setting it is always safe; ask for it where a malformed
	// answer would cost something, not everywhere.
	Schema *Schema
}

// Schema names a JSON shape a reply must take.
type Schema struct {
	// Name identifies the shape to the provider; letters and underscores.
	Name string
	// JSON is the JSON Schema itself.
	JSON json.RawMessage
	// Strict asks the provider to guarantee conformance rather than
	// merely aim at it, where it can.
	Strict bool
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

// Add accumulates u2 into u. The fields come from a provider response —
// user-hosted input, for the OpenAI-compatible dialects this client
// deliberately supports — and a broken or hostile endpoint can send
// negative or NaN numbers. Accumulated they corrupt the totals, /stats,
// and the budget check alike: a negative running cost never reaches the
// ceiling, so --max-cost would go unenforced. Anything that is not a
// finite non-negative number is dropped.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += max(u2.InputTokens, 0)
	u.OutputTokens += max(u2.OutputTokens, 0)
	u.CacheReadTokens += max(u2.CacheReadTokens, 0)
	u.CacheWriteTokens += max(u2.CacheWriteTokens, 0)
	if u2.CostUSD >= 0 && !math.IsNaN(u2.CostUSD) && !math.IsInf(u2.CostUSD, 0) {
		u.CostUSD += u2.CostUSD
	}
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

// CostSource says where a cost figure comes from: "reported" by the
// provider with the response, "list price" from a rate table, or
// "unpriced" when neither is available. A list-price figure is an
// API-equivalent estimate, not money spent.
func (u Usage) CostSource(p *Price) string {
	switch {
	case u.CostUSD > 0:
		return "reported"
	case p != nil:
		return "list price"
	}
	return "unpriced"
}

// TokenLine is the four token counts every usage view shows.
func (u Usage) TokenLine() string {
	return fmt.Sprintf("in %s · out %s · cache read %s · cache write %s",
		textfmt.Tokens(u.InputTokens), textfmt.Tokens(u.OutputTokens), textfmt.Tokens(u.CacheReadTokens), textfmt.Tokens(u.CacheWriteTokens))
}

// CacheHit is the share of the prompt tokens that were served from the
// provider's cache, and whether there is one to report: a model or
// endpoint that caches nothing has no rate, which is not the same as a
// rate of zero.
func (u Usage) CacheHit() (pct int, ok bool) {
	total := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	if total == 0 || u.CacheReadTokens+u.CacheWriteTokens == 0 {
		return 0, false
	}
	return u.CacheReadTokens * 100 / total, true
}

// CacheLine is TokenLine with the hit rate, for the views that have room
// for it. The rate is what the four counts are read for — whether the
// history is being paid for again — and it was left to the reader to
// divide.
func (u Usage) CacheLine() string {
	line := u.TokenLine()
	if pct, ok := u.CacheHit(); ok {
		line += fmt.Sprintf(" · cache hit %d%%", pct)
	}
	return line
}

// CostNote is the session cost with where it comes from, and when nothing
// prices the model, what to do about it.
func (u Usage) CostNote(p *Price) string {
	usd, _ := u.Cost(p)
	switch u.CostSource(p) {
	case "reported":
		return "cost " + FormatUSDPrecise(usd) + " (reported by the provider)"
	case "list price":
		return "cost " + FormatUSDPrecise(usd) + " at list price (an API-equivalent estimate from the config's \"prices\" or LiteLLM's rate table, not money spent)"
	}
	return "cost unpriced: the provider does not report it and no rate is known for this model; add it under \"prices\" in the config file, or wait for the rate table to be fetched"
}

// FormatUSD renders a cost with the fewest decimals that are honest:
// $3.40, $1.50, $0.25, $0.2, $0.123, and below a cent whatever
// FormatUSDPrecise needs to keep the figure from rounding away ($0.0023).
//
// Fixed decimals below a dollar printed three of them whatever the number
// was, so a quarter-dollar budget read "$0.250" — a figure with a
// precision it does not have, and the kind of thing a reader stops to
// parse. At or above a dollar two decimals are how money is written, so
// $1.50 keeps its trailing zero.
func FormatUSD(usd float64) string {
	switch {
	case usd < 0.01:
		return FormatUSDPrecise(usd)
	case usd < 1:
		s := strings.TrimRight(strconv.FormatFloat(usd, 'f', 3, 64), "0")
		if strings.HasSuffix(s, ".") {
			s += "00" // rounded up to a whole dollar
		}
		return "$" + s
	}
	return fmt.Sprintf("$%.2f", usd)
}

// FormatUSDPrecise renders a cost without rounding it out of existence.
//
// FormatUSD stops at four decimals, which is fine for a session total and
// useless for the figures a cheap model actually produces: a task that
// cost $0.00023 prints as "$0.0002", a $0.00005 budget as "$0.0000", and
// a message like "spent $0.0000 of $0.0000" says nothing about the thing
// it is reporting. Below a cent the number is given three significant
// digits with the trailing zeros trimmed, so $0.00023 stays $0.00023;
// above one, nothing changes.
func FormatUSDPrecise(usd float64) string {
	if usd < 0 {
		return "-" + FormatUSDPrecise(-usd)
	}
	if usd == 0 {
		return "$0.00"
	}
	if usd >= 0.01 {
		return FormatUSD(usd)
	}
	decimals := 2 - int(math.Floor(math.Log10(usd)))
	if decimals > 12 {
		decimals = 12
	}
	s := strconv.FormatFloat(usd, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		// Keep it looking like money even when it lands on a round number.
		if n := len(s) - strings.IndexByte(s, '.') - 1; n < 2 {
			s += strings.Repeat("0", 2-n)
		}
	}
	return "$" + s
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
	// Model is the model the provider says answered, when it says so at
	// all. A gateway is free to serve something other than what was asked
	// for, and an escalation that silently lands back on the cheap model
	// is a measurement error, not a saving -- so the served name is
	// carried back rather than assumed to equal the requested one.
	Model string
}

// EventType identifies a streaming event.
type EventType string

const (
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventToolUseStart  EventType = "tool_use_start"
	EventToolUseDelta  EventType = "tool_use_delta"
	EventToolUseEnd    EventType = "tool_use_end"
	// EventStatus reports what the provider is doing between attempts —
	// a retry and its countdown — so a stalled call does not look like a
	// slow one. Text carries the phrase to show; no content is implied.
	EventStatus EventType = "status"
	// EventRetried reports a call that failed, was retried and then
	// succeeded. EventStatus says the same thing while it is happening,
	// but it is a status line that the next one overwrites: once the call
	// lands there is no trace that it ever took four attempts and eight
	// seconds of waiting. Retry carries the count, the reason and the
	// wait, so a run can be read afterwards and say so.
	EventRetried EventType = "retried"
	// EventStreamDiscard says the deltas streamed so far in this call are
	// void: the attempt died without a terminal frame and the request is
	// being retried fresh, so consumers must throw the partial text away
	// rather than let the retried answer follow it on screen.
	EventStreamDiscard EventType = "stream_discard"
)

// Retry describes what one model call had to survive before it answered.
type Retry struct {
	// Attempts is how many retries were made, not counting the first try.
	Attempts int
	// Reason is why the last failure was worth retrying ("rate limited",
	// "server error"), in the provider's own words.
	Reason string
	// Wait is the total time spent in backoff between the attempts.
	Wait time.Duration
}

// Event is one streamed increment. Text carries the delta for text, thinking
// and partial tool-input JSON. Block is set on tool_use start (ID+Name) and
// end (complete Input). Retry is set on EventRetried.
type Event struct {
	Type  EventType
	Text  string
	Block *Block
	Retry *Retry
}

// Provider is a chat backend. Stream must call emit for every increment on the
// calling goroutine, honour ctx cancellation, and return the assembled
// response. emit may be nil.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error)
}

// PerTask is a provider that can hand out a sibling for one unit of
// concurrent work, sharing everything worth sharing (the endpoint, the key
// ring, whatever the server has taught us) but not the rate-limit pacing,
// which only ever widens and would otherwise let one refused task slow
// every other one down. Callers that run tasks in parallel in a single
// process should ask for one each; ForTaskOr does that where it exists and
// returns p unchanged where it does not.
type PerTask interface {
	Provider
	ForTask() Provider
}

// ForTaskOr returns a per-task sibling of p when p offers one.
func ForTaskOr(p Provider) Provider {
	if pt, ok := p.(PerTask); ok {
		return pt.ForTask()
	}
	return p
}

// ModelInfo is one model a provider offers.
type ModelInfo struct {
	ID   string
	Name string // display name when the provider gives one
	// ContextWindow is the model's input limit in tokens; 0 when unknown.
	ContextWindow int
	// MaxOutput is the largest completion the model can produce; 0 when unknown.
	MaxOutput int
	// Price per million tokens when the provider lists it.
	Price *Price
}

// KnownWindow guesses the context window of well-known model families
// whose listings do not include it. It returns 0 for anything else.
func KnownWindow(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return 200_000
	case strings.HasPrefix(m, "gpt-4o"), strings.HasPrefix(m, "gpt-4.1"), strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return 128_000
	}
	return 0
}

// ModelLister is implemented by providers that can enumerate their models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
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
		// An assistant turn may legitimately be empty — a model that
		// stopped without saying anything — and each provider has its own
		// placeholder for that. A user turn may not: with nothing to send,
		// the OpenAI path dropped the message entirely and left the
		// history with two assistant turns in a row or an assistant turn
		// last, which servers reject with an error about the history
		// rather than about the turn that broke it.
		if m.Role == RoleUser && !m.hasContent() {
			return fmt.Errorf("llm: user message at index %d has no content to send", i)
		}
	}
	return nil
}

// hasContent reports whether a message would put anything on the wire.
func (m Message) hasContent() bool {
	for _, b := range m.Blocks {
		switch b.Type {
		case BlockToolResult, BlockToolUse:
			return true
		case BlockImage:
			if b.Data != "" {
				return true
			}
		default:
			if b.Text != "" {
				return true
			}
		}
	}
	return false
}

// Embedder is implemented by providers that expose an embeddings endpoint.
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
}
