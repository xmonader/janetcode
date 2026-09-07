// Package anthropic implements llm.Provider on the official Anthropic Go SDK.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/xmonader/janetcode/internal/llm"
)

// RequestTimeout bounds one streamed call end to end.
const RequestTimeout = 30 * time.Minute

// Config configures the provider.
type Config struct {
	APIKey string
	// BaseURL overrides the API root (tests, proxies). Empty means default.
	BaseURL string
	// Thinking enables adaptive thinking. Set false for models that reject
	// the parameter (Haiku 4.5 and older).
	Thinking bool
	// Effort is one of low, medium, high, xhigh, max. Empty means the API
	// default.
	Effort string
	// MaxRetries for transient failures. Default 2.
	MaxRetries int
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

// Provider is the Anthropic Messages API backend.
type Provider struct {
	cfg    Config
	client sdk.Client
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, llm.ErrNoAPIKey
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	switch cfg.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return nil, fmt.Errorf("anthropic: invalid effort %q", cfg.Effort)
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
		// Long agentic turns can stream for many minutes on large models.
		option.WithRequestTimeout(RequestTimeout),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Provider{cfg: cfg, client: sdk.NewClient(opts...)}, nil
}

func (p *Provider) Name() string { return "anthropic" }

// APIError is a non-2xx response from the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }

func buildParams(req llm.Request, cfg Config) (sdk.MessageNewParams, error) {
	if err := req.Validate(); err != nil {
		return sdk.MessageNewParams{}, err
	}
	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		// Auto-place a cache breakpoint on the last cacheable block so the
		// growing conversation prefix is reused turn to turn.
		CacheControl: sdk.NewCacheControlEphemeralParam(),
	}
	if params.MaxTokens <= 0 {
		params.MaxTokens = 16000
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{
			Text:         req.System,
			CacheControl: sdk.NewCacheControlEphemeralParam(),
		}}
	}
	if cfg.Thinking {
		adaptive := sdk.ThinkingConfigAdaptiveParam{Display: sdk.ThinkingConfigAdaptiveDisplaySummarized}
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	}
	if cfg.Effort != "" {
		params.OutputConfig = sdk.OutputConfigParam{Effort: sdk.OutputConfigEffort(cfg.Effort)}
	}
	for _, t := range req.Tools {
		tp, err := toolParam(t)
		if err != nil {
			return sdk.MessageNewParams{}, err
		}
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: &tp})
	}
	for i, m := range req.Messages {
		var blocks []sdk.ContentBlockParamUnion
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if b.Text == "" {
					continue
				}
				blocks = append(blocks, sdk.NewTextBlock(b.Text))
			case llm.BlockThinking:
				// Thinking must be echoed back with its signature; unsigned
				// thinking (from another provider) is dropped.
				if b.Signature != "" {
					blocks = append(blocks, sdk.NewThinkingBlock(b.Signature, b.Text))
				}
			case llm.BlockToolUse:
				input := b.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, sdk.NewToolUseBlock(b.ID, input, b.Name))
			case llm.BlockToolResult:
				content := b.Text
				if content == "" {
					content = "(no output)"
				}
				blocks = append(blocks, sdk.NewToolResultBlock(b.ToolUseID, content, b.IsError))
			}
		}
		if len(blocks) == 0 {
			// The API rejects empty content; keep the history replayable.
			if m.Role == llm.RoleAssistant {
				blocks = append(blocks, sdk.NewTextBlock("(empty response)"))
			} else {
				return sdk.MessageNewParams{}, fmt.Errorf("anthropic: message %d has no sendable content", i)
			}
		}
		if m.Role == llm.RoleAssistant {
			params.Messages = append(params.Messages, sdk.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, sdk.NewUserMessage(blocks...))
		}
	}
	return params, nil
}

func toolParam(t llm.ToolDef) (sdk.ToolParam, error) {
	var schema map[string]any
	if len(t.InputSchema) > 0 {
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return sdk.ToolParam{}, fmt.Errorf("anthropic: tool %s schema: %w", t.Name, err)
		}
	}
	is := sdk.ToolInputSchemaParam{}
	if props, ok := schema["properties"]; ok {
		is.Properties = props
	} else {
		is.Properties = map[string]any{}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				is.Required = append(is.Required, s)
			}
		}
	}
	for k, v := range schema {
		if k == "type" || k == "properties" || k == "required" {
			continue
		}
		if is.ExtraFields == nil {
			is.ExtraFields = map[string]any{}
		}
		is.ExtraFields[k] = v
	}
	return sdk.ToolParam{
		Name:        t.Name,
		Description: sdk.String(t.Description),
		InputSchema: is,
	}, nil
}

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	params, err := buildParams(req, p.cfg)
	if err != nil {
		return nil, err
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	msg := sdk.Message{}
	// Track open tool_use blocks by index so we can emit start/end events.
	openTools := map[int64]*llm.Block{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("anthropic: accumulate: %w", err)
		}
		switch v := ev.AsAny().(type) {
		case sdk.ContentBlockStartEvent:
			if v.ContentBlock.Type == "tool_use" {
				b := &llm.Block{Type: llm.BlockToolUse, ID: v.ContentBlock.ID, Name: v.ContentBlock.Name}
				openTools[v.Index] = b
				emit(llm.Event{Type: llm.EventToolUseStart, Block: b})
			}
		case sdk.ContentBlockDeltaEvent:
			switch d := v.Delta.AsAny().(type) {
			case sdk.TextDelta:
				emit(llm.Event{Type: llm.EventTextDelta, Text: d.Text})
			case sdk.ThinkingDelta:
				emit(llm.Event{Type: llm.EventThinkingDelta, Text: d.Thinking})
			case sdk.InputJSONDelta:
				emit(llm.Event{Type: llm.EventToolUseDelta, Text: d.PartialJSON})
			}
		case sdk.ContentBlockStopEvent:
			if b, ok := openTools[v.Index]; ok {
				if int(v.Index) < len(msg.Content) {
					b.Input = msg.Content[v.Index].Input
				}
				emit(llm.Event{Type: llm.EventToolUseEnd, Block: b})
				delete(openTools, v.Index)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, mapError(ctx, err)
	}
	return toResponse(msg)
}

func mapError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *sdk.Error
	if errors.As(err, &ae) {
		msg := http.StatusText(ae.StatusCode)
		var env struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ae.RawJSON()), &env) == nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		if ae.RequestID != "" {
			msg += " (request " + ae.RequestID + ")"
		}
		return &APIError{Status: ae.StatusCode, Message: msg}
	}
	return err
}

func toResponse(msg sdk.Message) (*llm.Response, error) {
	out := llm.Message{Role: llm.RoleAssistant}
	for _, c := range msg.Content {
		switch v := c.AsAny().(type) {
		case sdk.TextBlock:
			out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockText, Text: v.Text})
		case sdk.ThinkingBlock:
			out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockThinking, Text: v.Thinking, Signature: v.Signature})
		case sdk.RedactedThinkingBlock:
			// Cannot be represented; the API accepts history without it.
		case sdk.ToolUseBlock:
			input := json.RawMessage(v.JSON.Input.Raw())
			if len(input) == 0 || !json.Valid(input) {
				return nil, fmt.Errorf("anthropic: tool call %s has invalid JSON input: %.200s", v.Name, string(input))
			}
			out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockToolUse, ID: v.ID, Name: v.Name, Input: input})
		}
	}
	if len(out.Blocks) == 0 {
		if msg.StopReason == "" {
			return nil, errors.New("anthropic: stream ended without a message")
		}
		out.Blocks = []llm.Block{{Type: llm.BlockText, Text: ""}}
	}
	var stop llm.StopReason
	switch msg.StopReason {
	case sdk.StopReasonEndTurn, sdk.StopReasonStopSequence:
		stop = llm.StopEndTurn
	case sdk.StopReasonToolUse:
		stop = llm.StopToolUse
	case sdk.StopReasonMaxTokens:
		stop = llm.StopMaxTokens
	case sdk.StopReasonRefusal:
		stop = llm.StopRefusal
	default:
		stop = llm.StopOther
	}
	return &llm.Response{
		Message:    out,
		StopReason: stop,
		Usage: llm.Usage{
			InputTokens:      int(msg.Usage.InputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CacheReadTokens:  int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
		},
	}, nil
}
