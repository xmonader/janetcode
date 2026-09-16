// Package anthropic implements llm.Provider on the official Anthropic Go SDK.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
	// APIKeys are extra keys tried in turn when one is rate limited or
	// rejected.
	APIKeys []string
	// BaseURL overrides the API root (tests, proxies). Empty means default.
	BaseURL string
	// Thinking enables adaptive thinking. Set false for models that reject
	// the parameter (Haiku 4.5 and older).
	Thinking bool
	// HideThinking asks the provider to omit the reasoning text while
	// still returning its signature, for a UI that does not show it.
	HideThinking bool
	// Effort is one of low, medium, high, xhigh, max. Empty means the API
	// default.
	Effort string
	// Limits bound one streamed call: a resettable idle timeout and a
	// lifetime cap. The zero value means the defaults.
	Limits llm.StreamLimits
	// MaxRetries for transient failures. Zero means the default of 2;
	// negative means no retries at all, so that "do not retry" is
	// expressible and does not read as "use the default" the way a plain
	// 0 did.
	MaxRetries int
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

// Provider is the Anthropic Messages API backend.
type Provider struct {
	// mu guards the clamp: the tier a provider refused (asked) and what
	// was used instead (clamped). It is kept for the provider's lifetime
	// so the refusal is paid for once — but only for the tier it was
	// learned about, so asking for another tier tries it honestly.
	mu       sync.Mutex
	asked    string
	clamped  string
	hasClamp bool

	cfg     Config
	client  sdk.Client // first key's client; kept for tests
	clients []sdk.Client
	ring    *llm.KeyRing
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	ring := llm.NewKeyRing(append([]string{cfg.APIKey}, cfg.APIKeys...))
	keys := ring.Keys()
	if keys[0] == "" {
		return nil, llm.ErrNoAPIKey
	}
	cfg.APIKey = keys[0]
	switch {
	case cfg.MaxRetries == 0:
		cfg.MaxRetries = 2
	case cfg.MaxRetries < 0:
		cfg.MaxRetries = 0
	}
	if !llm.ValidEffort(cfg.Effort) {
		return nil, fmt.Errorf("anthropic: invalid effort %q", cfg.Effort)
	}
	p := &Provider{cfg: cfg, ring: ring}
	for _, key := range keys {
		opts := []option.RequestOption{
			option.WithAPIKey(key),
			// The SDK does not retry: retries hidden inside it are
			// invisible to the stream watchdog and to the events a UI
			// draws, so a long Retry-After there reads as a stalled turn
			// with no countdown. Stream runs the shared llm.Retrier
			// instead, which is the same policy the other providers
			// follow (cfg.MaxRetries is its budget).
			option.WithMaxRetries(0),
			// Long agentic turns can stream for many minutes on large models.
			option.WithRequestTimeout(RequestTimeout),
		}
		if cfg.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(cfg.BaseURL))
		}
		if cfg.HTTPClient != nil {
			opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
		}
		p.clients = append(p.clients, sdk.NewClient(opts...))
	}
	p.client = p.clients[0]
	return p, nil
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return "anthropic" }

// SupportsEffort implements llm.EffortAware: the Messages API takes a
// reasoning tier on output_config, so llm.Request.Effort is sent.
func (p *Provider) SupportsEffort() bool { return true }

// Keys is the number of distinct API keys configured.
func (p *Provider) Keys() int { return p.ring.Len() }

// current returns the client for the key the ring picks now.
func (p *Provider) current() (*sdk.Client, int) {
	_, i := p.ring.Current()
	return &p.clients[i], i
}

// keyProblem reports whether an error is one another key might avoid.
func keyProblem(err error) (llm.Failure, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	switch ae.Status {
	case 429, 401, 403, 402, 529:
		return llm.ClassifyFailure(ae.Status, ae.Message), true
	}
	return 0, false
}

// ListModels implements llm.ModelLister.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	client, _ := p.current()
	page, err := client.Models.List(ctx, sdk.ModelListParams{Limit: sdk.Int(100)})
	if err != nil {
		return nil, mapError(ctx, err)
	}
	var out []llm.ModelInfo
	for page != nil {
		for _, m := range page.Data {
			out = append(out, llm.ModelInfo{ID: m.ID, Name: m.DisplayName})
		}
		page, err = page.GetNextPage()
		if err != nil {
			return nil, mapError(ctx, err)
		}
	}
	return out, nil
}

// APIError is a non-2xx response from the API.
type APIError struct {
	Status  int
	Message string
	// RetryAfter is the server's own Retry-After, kept only for the two
	// statuses that mean "come back later" — 429 and 503. On anything
	// else the header is advice about a failure that is not going to
	// clear (see llm.RetryAfterFrom).
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }

// HTTPStatus and RetryHint implement llm.StatusError, which is how the
// shared retry policy judges an error from this provider.
func (e *APIError) HTTPStatus() int          { return e.Status }
func (e *APIError) RetryHint() time.Duration { return e.RetryAfter }

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
		params.System = systemBlocks(req.System)
	}
	if cfg.Thinking {
		// Omitted still returns the signature that keeps later turns
		// valid; it only drops the text, which is worth doing when
		// nothing renders it — the first visible token arrives sooner and
		// the reasoning is not billed as output.
		display := sdk.ThinkingConfigAdaptiveDisplaySummarized
		if cfg.HideThinking {
			display = sdk.ThinkingConfigAdaptiveDisplayOmitted
		}
		adaptive := sdk.ThinkingConfigAdaptiveParam{Display: display}
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
				blocks = append(blocks, sdk.NewToolUseBlock(b.WireID(), input, b.Name))
			case llm.BlockToolResult:
				content := b.Text
				if content == "" {
					content = "(no output)"
				}
				blocks = append(blocks, sdk.NewToolResultBlock(b.ToolUseID, content, b.IsError))
			case llm.BlockImage:
				blocks = append(blocks, sdk.NewImageBlockBase64(b.MediaType, b.Data))
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

// envSection is the heading prompt.System writes cwd, date, git state and
// the project's own instructions under. Everything above it is the same
// for every session on this build; everything below it changes with the
// directory, the day and the branch. The OpenAI path splits there for
// the same reason (see openai.applyCacheBreakpoints).
const envSection = "# Environment\n"

// systemBlocks is the system prompt as cacheable blocks. Splitting it at
// the environment heading gives the static instructions a cache entry of
// their own, so the big prefix survives what the tail does not: a `cd`
// that rewrites the environment, a subagent that appends its brief, an
// eval task in another working directory. Both blocks are marked, so a
// session that never moves still reads the whole prompt from cache.
func systemBlocks(system string) []sdk.TextBlockParam {
	if i := strings.Index(system, envSection); i > 0 {
		return []sdk.TextBlockParam{
			{Text: system[:i], CacheControl: sdk.NewCacheControlEphemeralParam()},
			{Text: system[i:], CacheControl: sdk.NewCacheControlEphemeralParam()},
		}
	}
	return []sdk.TextBlockParam{{Text: system, CacheControl: sdk.NewCacheControlEphemeralParam()}}
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
	// A stream that stops producing without failing would otherwise hang
	// the turn for as long as the process lives.
	ctx, wd := llm.Watch(ctx, p.cfg.Limits)
	defer wd.Stop()
	// The same retry policy every provider follows: the SDK's own is
	// turned off (see New), so the backoff, the countdown and the summary
	// all happen where the watchdog and the UI can see them.
	rt := llm.NewRetrier(p.cfg.MaxRetries, emit, wd)
	rotations := 0
	for {
		cfg := p.config()
		// The tier is read per request, not per provider: /effort changes
		// the agent's live setting and the very next call carries it.
		cfg.Effort = p.effortFor(req.Effort)
		params, err := buildParams(req, cfg)
		if err != nil {
			return nil, err
		}
		client, idx := p.current()
		resp, produced, err := p.stream(ctx, client, params, wd, emit)
		if err == nil {
			p.ring.OK(idx)
			rt.Landed()
			return resp, nil
		}
		// p.stream never returns a response alongside an error; say so
		// rather than handing the caller a half-response to guess about.
		resp = nil
		if produced {
			// The failure came after the model had already said
			// something. Sending the request again would replay that
			// text into the turn, so the call fails and the user resends.
			return nil, wd.Wrap(err)
		}
		if p.clampEffort(err) {
			// The model does not go that high; the next tier down is
			// what the user asked for as nearly as this model can, and
			// it is remembered so it is paid for once. It is not a
			// transient failure, so it spends no retry budget.
			continue
		}
		if f, ok := keyProblem(err); ok && p.ring.Fail(idx, f) && rotations < p.ring.Len()-1 {
			// Another key may have quota left; try it right away.
			rotations++
			continue
		}
		if werr := rt.Wait(ctx, err); werr != nil {
			return nil, wd.Wrap(werr)
		}
	}
}

// config is the configuration as it stands. The effort tier is not part
// of it: it is resolved per request by effortFor.
func (p *Provider) config() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

// effortFor resolves the tier one request will actually ask for: the
// request's own tier, or the configured one when it names none, lowered
// to whatever this model was last willing to take. A different tier from
// the one the clamp was learned about discards the clamp, so a person who
// asks for another tier after a refusal is asking honestly and gets it
// tried — a manual change always wins over the automatic downgrade.
func (p *Provider) effortFor(want string) string {
	if want == "" {
		want = p.cfg.Effort
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if want != p.asked {
		p.asked, p.clamped, p.hasClamp = want, "", false
	}
	if p.hasClamp {
		return p.clamped
	}
	return want
}

// clampEffort lowers the reasoning tier by one rank when the provider
// rejected the one asked for, and reports whether the request should be
// sent again. Below the lowest tier the parameter is dropped entirely.
func (p *Provider) clampEffort(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 {
		return false
	}
	msg := strings.ToLower(ae.Message)
	if !strings.Contains(msg, "effort") && !strings.Contains(msg, "output_config") {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.asked
	switch {
	case p.hasClamp:
		current = p.clamped
	case current == "":
		// No request has resolved a tier yet (a refusal on the very
		// first call is answered before effortFor ran in a test): the
		// configured tier is what was asked for.
		current = p.cfg.Effort
		p.asked = current
	}
	if current == "" {
		return false // nothing was asked for; the complaint is about something else
	}
	p.clamped, p.hasClamp = "", true
	if rank := llm.EffortRank(current); rank > 0 {
		p.clamped = llm.EffortTiers[rank-1]
	}
	return true
}

// stream runs one attempt. produced says whether the model had begun
// answering before any error: past that point the attempt cannot be
// retried without replaying what was already emitted.
func (p *Provider) stream(ctx context.Context, client *sdk.Client, params sdk.MessageNewParams, wd *llm.Watchdog, emit func(llm.Event)) (resp *llm.Response, produced bool, err error) {
	stream := client.Messages.NewStreaming(ctx, params)
	msg := sdk.Message{}
	// Track open tool_use blocks by index so we can emit start/end events.
	openTools := map[int64]*llm.Block{}
	for stream.Next() {
		wd.Tick()
		produced = true
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, produced, fmt.Errorf("anthropic: accumulate: %w", err)
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
		return nil, produced, mapError(ctx, err)
	}
	out, err := toResponse(msg)
	return out, produced, err
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
		out := &APIError{Status: ae.StatusCode, Message: msg}
		if ae.Response != nil {
			out.RetryAfter = llm.RetryAfterFrom(ae.Response.Header, ae.StatusCode)
		}
		return out
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
			// A call with no name or no id cannot be run, and going back
			// into the history it makes every later turn invalid.
			if v.Name == "" {
				return nil, fmt.Errorf("anthropic: tool call %q has no name", v.ID)
			}
			if v.ID == "" {
				return nil, fmt.Errorf("anthropic: tool call %s has no id", v.Name)
			}
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
