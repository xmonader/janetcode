package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type wireMessage struct {
	Role string `json:"role"`
	// Content is a string, or []wirePart when the message carries images.
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wirePart is one element of a multi-part user message.
type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	// CacheControl is an Anthropic-style breakpoint. DashScope and OpenRouter
	// honour it; strict local servers must not see it (see applyCacheBreakpoints).
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

type wireCacheControl struct {
	Type string `json:"type"`
}

type wireToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
	// CacheControl on the last tool includes the tool schemas in DashScope's
	// cached prefix (same marker qwen-code sends).
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	// Newer OpenAI models reject max_tokens and want max_completion_tokens;
	// most other servers only know max_tokens. Which one is sent is decided
	// per provider by dialect detection (see Provider.useMaxCompletion).
	MaxTokens           int  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int  `json:"max_completion_tokens,omitempty"`
	Stream              bool `json:"stream"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	// There is deliberately no reasoning_effort here, and llm.Request's
	// Effort is ignored (this provider does not implement
	// llm.EffortAware). The field is OpenAI's own, its values are a
	// shorter ladder than llm.EffortTiers, and the servers this one
	// talks to — local runtimes, DashScope, OpenRouter, Ollama — variously
	// do not know it, spell it differently (OpenRouter nests it under
	// "reasoning") or reject the whole request for an unknown field, the
	// way response_format has to be retried without below. Wiring it
	// would need per-dialect detection and a rejection retry of its own;
	// until then the tier applies to Anthropic models and /effort says so.
	ResponseFormat *wireResponseFormat `json:"response_format,omitempty"`
	// PromptCacheKey routes the request to a cache shard. Without a stable
	// key an agent loop's growing prefix can be load-balanced onto a
	// different shard from one turn to the next and re-billed as fresh, so
	// the session id is sent when the server understands the field.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

// wireResponseFormat constrains the reply to a JSON shape. Servers that
// do not know the field ignore it; one that rejects the whole request for
// it is handled by retrying without (see noResponseFormat).
type wireResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *wireJSONSchema `json:"json_schema,omitempty"`
}

type wireJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict,omitempty"`
}

type wireChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          wireContent         `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			Reasoning        string              `json:"reasoning"`
			ReasoningDetails []wireReasoningPart `json:"reasoning_details"`
			ToolCalls        []wireDeltaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *wireError `json:"error"`
}

// wireDeltaToolCall is one tool-call fragment in a streamed delta.
// wireReasoningPart is one OpenRouter/Z.AI reasoning fragment.
type wireReasoningPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Summary string `json:"summary"`
}

type wireDeltaToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireUsage struct {
	PromptTokens        looseInt `json:"prompt_tokens"`
	CompletionTokens    looseInt `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens looseInt `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	Cost looseFloat `json:"cost"` // OpenRouter reports the request price
}

// A chunk that fails to decode fails the stream as truncated, and the
// retry gets the same bytes back. The types below accept the shapes
// servers are known to send for a field besides the standard one, so a
// well-formed reply is not lost to a type mismatch.

// wireContent is a delta's content: a string, null, or — Mistral's
// reasoning models — a list of parts, where a "thinking" part carries
// reasoning and the rest carry text.
type wireContent struct {
	Text     string
	Thinking string
}

func (c *wireContent) UnmarshalJSON(b []byte) error {
	*c = wireContent{}
	switch firstNonSpace(b) {
	case 'n':
		return nil
	case '"':
		return json.Unmarshal(b, &c.Text)
	case '[':
		var parts []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking json.RawMessage `json:"thinking"`
		}
		if err := json.Unmarshal(b, &parts); err != nil {
			return err
		}
		for _, p := range parts {
			if p.Type == "thinking" {
				var inner wireContent
				if len(p.Thinking) > 0 && inner.UnmarshalJSON(p.Thinking) == nil {
					c.Thinking += inner.Text + inner.Thinking
				}
				continue
			}
			c.Text += p.Text
		}
		return nil
	}
	return fmt.Errorf("content: unexpected JSON %.40s", b)
}

// looseInt is a token count sent as a number, a numeric string or null.
type looseInt int

func (n *looseInt) UnmarshalJSON(b []byte) error {
	var f looseFloat
	if err := f.UnmarshalJSON(b); err != nil {
		return err
	}
	*n = looseInt(f)
	return nil
}

// looseFloat is a number sent as a number, a numeric string or null.
type looseFloat float64

func (f *looseFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*f = 0
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*f = 0
			return nil
		}
		b = []byte(strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("number: %w", err)
	}
	*f = looseFloat(v)
	return nil
}

func firstNonSpace(b []byte) byte {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// APIError is a non-2xx response from the server.
type APIError struct {
	Status  int
	Message string
	// Detail carries the same server text redacted but uncapped, so
	// dialect learning and classification can read past Message's display
	// cap. It is never rendered: Error() and every caller print Message
	// only.
	Detail string
	// RetryAfter is the server's own Retry-After, kept only for the two
	// statuses that mean "come back later" — 429 and 503. On anything
	// else the header is advice about a failure that is not going to
	// clear, and honouring it only adds the wait to the error.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

// ErrorDetail implements llm.DetailedError: the whole server text, for
// the decisions that must read past Message's display cap.
func (e *APIError) ErrorDetail() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Message
}

// HTTPStatus and RetryHint implement llm.StatusError, which is how the
// shared retry policy judges an error from this provider.
func (e *APIError) HTTPStatus() int          { return e.Status }
func (e *APIError) RetryHint() time.Duration { return e.RetryAfter }
