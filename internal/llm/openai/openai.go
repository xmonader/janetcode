// Package openai implements llm.Provider over the OpenAI chat-completions
// wire format, which OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM and Ollama
// all speak. The client is hand-rolled so it stays tolerant of the small
// dialect differences between those servers.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// RequestTimeout bounds one streamed call end to end.
const RequestTimeout = 30 * time.Minute

// Config configures the provider.
type Config struct {
	// BaseURL is the API root, e.g. https://api.openai.com/v1.
	BaseURL string
	APIKey  string
	// APIKeys are extra keys tried in turn when one is rate limited or
	// rejected (429, 401, 403, quota errors).
	APIKeys []string
	// Name is the label shown to users (default "openai").
	Name string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
	// MaxRetries for connection errors, 429 and 5xx before any bytes are
	// streamed. Default 2.
	MaxRetries int
	// Limits bound one streamed call: a resettable idle timeout and a
	// lifetime cap. The zero value means the defaults.
	Limits llm.StreamLimits
	// CacheKey is a stable per-session string sent as prompt_cache_key.
	// Servers that shard their prompt cache hash the prefix together with
	// this key, so without one a session's growing prefix can land on a
	// different shard each turn and be billed as fresh. Empty means the
	// field is not sent; a server that rejects it is not asked twice.
	CacheKey string
}

// Provider is an OpenAI-compatible chat backend.
type Provider struct {
	cfg  Config
	ring *llm.KeyRing
	http *http.Client
	// pace widens the gap between requests after the provider starts
	// refusing them, so a long run stops walking into the same limit.
	pace llm.Pacer

	// Dialect quirks learned from 400 responses, kept for the provider's
	// lifetime so each is paid for at most once.
	mu               sync.Mutex
	useMaxCompletion bool // send max_completion_tokens instead of max_tokens
	noStreamOptions  bool // server rejects stream_options
	noCacheControl   bool // server rejects cache_control / array content
	noCacheKey       bool // server rejects prompt_cache_key
	noJSONSchema     bool // server rejects response_format json_schema
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	ring := llm.NewKeyRing(append([]string{cfg.APIKey}, cfg.APIKeys...))
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.CacheKey == "" && shardsPromptCache(cfg.BaseURL) {
		cfg.CacheKey = defaultCacheKey()
	}
	// Local servers (Ollama, LM Studio) accept requests without a key;
	// hosted ones never do, so refuse early with a clear error.
	if k, _ := ring.Current(); k == "" && !localURL(cfg.BaseURL) {
		return nil, llm.ErrNoAPIKey
	}
	cfg.APIKey, _ = ring.Current()
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// Agentic turns on large models can stream for a long time; bound
		// the wait for headers, and let ctx cancel a stalled body.
		hc = &http.Client{
			Timeout:   RequestTimeout,
			Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute, Proxy: http.ProxyFromEnvironment},
		}
	}
	return &Provider{cfg: cfg, ring: ring, http: hc}, nil
}

// localURL reports whether u points at this machine.
func localURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := parsed.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0"
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return p.cfg.Name }

// Keys is the number of distinct API keys configured.
func (p *Provider) Keys() int { return p.ring.Len() }

// keyProblem reports whether an error is one another key might avoid, and
// how to classify it for the key ring.
func keyProblem(err error) (llm.Failure, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	msg := strings.ToLower(ae.Message)
	switch {
	case ae.Status == 429 || ae.Status == 401 || ae.Status == 403 || ae.Status == 402:
	case strings.Contains(msg, "quota") || strings.Contains(msg, "rate limit"):
	default:
		return 0, false
	}
	return llm.ClassifyFailure(ae.Status, ae.Message), true
}

// Embed implements llm.Embedder via POST /embeddings.
func (p *Provider) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": texts})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if k, _ := p.ring.Current(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := p.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embeddings: bad response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for i, d := range out.Data {
		idx := d.Index
		if idx < 0 || idx >= len(texts) {
			idx = i
		}
		vecs[idx] = d.Embedding
	}
	return vecs, nil
}

// ListModels implements llm.ModelLister via GET /models.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if k, _ := p.ring.Current(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := p.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	var out struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"` // vLLM
			TopProvider   struct {
				ContextLength       int `json:"context_length"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Pricing map[string]json.RawMessage `json:"pricing"` // OpenRouter: USD per token, as strings
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("models: bad response: %w", err)
	}
	var models []llm.ModelInfo
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		mi := llm.ModelInfo{ID: m.ID, Name: m.Name, ContextWindow: m.ContextLength, MaxOutput: m.TopProvider.MaxCompletionTokens}
		if mi.ContextWindow == 0 {
			mi.ContextWindow = m.TopProvider.ContextLength
		}
		if mi.ContextWindow == 0 {
			mi.ContextWindow = m.MaxModelLen
		}
		if p, ok := parsePricing(m.Pricing); ok {
			mi.Price = &p
		}
		models = append(models, mi)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// --- wire types ---

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
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		Cost float64 `json:"cost"` // OpenRouter reports the request price
	} `json:"usage"`
	Error *wireError `json:"error"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// parsePricing reads OpenRouter's per-token USD strings into a per-million
// Price. It reports false when no prompt or completion price is present.
func parsePricing(pricing map[string]json.RawMessage) (llm.Price, bool) {
	get := func(k string) (float64, bool) {
		r, ok := pricing[k]
		if !ok {
			return 0, false
		}
		var s string
		var f float64
		switch {
		case json.Unmarshal(r, &s) == nil:
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return 0, false
			}
			f = v
		case json.Unmarshal(r, &f) == nil:
		default:
			return 0, false
		}
		if f < 0 {
			return 0, false
		}
		return math.Round(f*1e6*1e6) / 1e6, true // per million, 6 decimals
	}
	in, okIn := get("prompt")
	out, okOut := get("completion")
	if !okIn && !okOut {
		return llm.Price{}, false
	}
	p := llm.Price{Input: in, Output: out}
	p.CacheRead, _ = get("input_cache_read")
	p.CacheWrite, _ = get("input_cache_write")
	return p, true
}

// APIError is a non-2xx response from the server.
type APIError struct {
	Status  int
	Message string
	// RetryAfter is the server's own Retry-After, kept only for the two
	// statuses that mean "come back later" — 429 and 503. On anything
	// else the header is advice about a failure that is not going to
	// clear, and honouring it only adds the wait to the error.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

// --- request building ---

func buildRequest(req llm.Request) (wireRequest, error) {
	if err := req.Validate(); err != nil {
		return wireRequest{}, err
	}
	w := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens, Stream: true}
	if req.Schema != nil && len(req.Schema.JSON) > 0 {
		w.ResponseFormat = &wireResponseFormat{
			Type:       "json_schema",
			JSONSchema: &wireJSONSchema{Name: req.Schema.Name, Schema: req.Schema.JSON, Strict: req.Schema.Strict},
		}
	}
	w.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{true}
	if req.System != "" {
		w.Messages = append(w.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleUser:
			// Tool results and plain text can both sit in a user turn; emit
			// tool results as "tool" messages and text as a "user" message.
			var (
				text   strings.Builder
				images []wirePart
			)
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockToolResult:
					content := b.Text
					if b.IsError {
						content = "Error: " + content
					}
					w.Messages = append(w.Messages, wireMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
				case llm.BlockText:
					text.WriteString(b.Text)
				case llm.BlockImage:
					part := wirePart{Type: "image_url"}
					part.ImageURL = &struct {
						URL string `json:"url"`
					}{"data:" + b.MediaType + ";base64," + b.Data}
					images = append(images, part)
				}
			}
			switch {
			case len(images) > 0:
				parts := []wirePart{}
				if text.Len() > 0 {
					parts = append(parts, wirePart{Type: "text", Text: text.String()})
				}
				w.Messages = append(w.Messages, wireMessage{Role: "user", Content: append(parts, images...)})
			case text.Len() > 0:
				w.Messages = append(w.Messages, wireMessage{Role: "user", Content: text.String()})
			}
		case llm.RoleAssistant:
			am := wireMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockText:
					text.WriteString(b.Text)
				case llm.BlockToolUse:
					tc := wireToolCall{ID: b.WireID(), Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			am.Content = text.String()
			w.Messages = append(w.Messages, am)
		default:
			return wireRequest{}, fmt.Errorf("openai: unsupported role %q", m.Role)
		}
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.InputSchema
		if len(wt.Function.Parameters) == 0 {
			wt.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		w.Tools = append(w.Tools, wt)
	}
	return w, nil
}

// shardsPromptCache is true for endpoints that route to a prompt-cache
// shard by hashing the prefix together with prompt_cache_key. Sending the
// field elsewhere risks a 400 mid-session for no benefit, which is worse
// than the cache miss it would avoid.
func shardsPromptCache(base string) bool {
	u := strings.ToLower(base)
	return strings.Contains(u, "api.openai.com") || strings.Contains(u, "openrouter.ai")
}

// defaultCacheKey is stable for the life of the process, which is the life
// of a session. It only has to be the same from one turn to the next; it
// is not a secret and identifies nothing.
func defaultCacheKey() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("janetcode-%d", time.Now().UnixNano())
	}
	return "janetcode-" + hex.EncodeToString(b[:])
}

// cacheableURL is true for endpoints that honour Anthropic-style
// cache_control breakpoints (and, for DashScope, need an opt-in header).
func cacheableURL(base string) bool {
	u := strings.ToLower(base)
	return strings.Contains(u, "dashscope") || strings.Contains(u, "openrouter.ai")
}

func extraHeaders(baseURL string) map[string]string {
	if strings.Contains(strings.ToLower(baseURL), "dashscope") {
		return map[string]string{"X-DashScope-CacheControl": "enable"}
	}
	return nil
}

func normalizeContent(content any) []wirePart {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []wirePart{{Type: "text", Text: c}}
	case []wirePart:
		return c
	default:
		return nil
	}
}

func markBreakpoint(m *wireMessage) {
	parts := normalizeContent(m.Content)
	if len(parts) == 0 {
		return
	}
	last := parts[len(parts)-1]
	if last.Type != "text" {
		return
	}
	cc := wireCacheControl{Type: "ephemeral"}
	last.CacheControl = &cc
	parts[len(parts)-1] = last
	m.Content = parts
}

// envSection is the heading prompt.System uses for cwd/date/git. Everything
// before it is identical across working directories and can be cached;
// everything after it changes every eval workdir and must stay uncached.
const envSection = "# Environment\n"

// applyCacheBreakpoints puts ephemeral cache_control markers on the stable
// prefix: static system instructions (not cwd/date), the last tool schema,
// then the last content-bearing history message that is not the tail.
func applyCacheBreakpoints(w *wireRequest) {
	for i := range w.Messages {
		if parts := normalizeContent(w.Messages[i].Content); len(parts) > 0 {
			w.Messages[i].Content = parts
		}
	}
	for i := range w.Messages {
		if w.Messages[i].Role == "system" {
			markStableSystem(&w.Messages[i])
			break
		}
	}
	if n := len(w.Tools); n > 0 {
		cc := wireCacheControl{Type: "ephemeral"}
		w.Tools[n-1].CacheControl = &cc
	}
	if len(w.Messages) < 2 {
		return
	}
	for i := len(w.Messages) - 2; i >= 0; i-- {
		if w.Messages[i].Role == "system" {
			continue
		}
		if len(normalizeContent(w.Messages[i].Content)) == 0 {
			continue
		}
		markBreakpoint(&w.Messages[i])
		break
	}
}

func markStableSystem(m *wireMessage) {
	parts := normalizeContent(m.Content)
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		text := parts[0].Text
		if i := strings.Index(text, envSection); i > 0 {
			m.Content = []wirePart{
				{Type: "text", Text: text[:i], CacheControl: &wireCacheControl{Type: "ephemeral"}},
				{Type: "text", Text: text[i:]},
			}
			return
		}
	}
	markBreakpoint(m)
}

func stripCacheControl(w *wireRequest) {
	for i := range w.Tools {
		w.Tools[i].CacheControl = nil
	}
	for i := range w.Messages {
		parts := normalizeContent(w.Messages[i].Content)
		if len(parts) == 0 {
			continue
		}
		for j := range parts {
			parts[j].CacheControl = nil
		}
		if len(parts) == 1 && parts[0].Type == "text" && parts[0].ImageURL == nil {
			w.Messages[i].Content = parts[0].Text
			continue
		}
		w.Messages[i].Content = parts
	}
}

// --- streaming ---

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	// A stream that stops producing without failing would otherwise hang
	// the turn for as long as the process lives.
	ctx, wd := llm.Watch(ctx, p.cfg.Limits)
	defer wd.Stop()
	// Spacing imposed by earlier rate limits, if any. Costs nothing on a
	// session the provider has never refused.
	if err := p.pace.Wait(ctx); err != nil {
		return nil, err
	}
	w, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	w.PromptCacheKey = p.cfg.CacheKey
	if cacheableURL(p.cfg.BaseURL) {
		applyCacheBreakpoints(&w)
	}
	var resp *http.Response
	rotations := 0
	for attempt := 0; ; attempt++ {
		body, err := json.Marshal(p.applyDialect(w))
		if err != nil {
			return nil, err
		}
		key, idx := p.ring.Current()
		resp, err = p.do(ctx, body, key)
		if err == nil {
			p.ring.OK(idx)
			break
		}
		if p.learnDialect(err) {
			// A parameter the server rejects was dropped or renamed; resend
			// immediately without counting it as a retry. continue runs
			// the post statement, so attempt has to be put back — as the
			// key rotation below does — or working out the dialect spends
			// the budget meant for transient failures.
			attempt--
			continue
		}
		if f, ok := keyProblem(err); ok {
			// Remember the bad key; another may have quota left, so try it
			// right away without counting a retry.
			if p.ring.Fail(idx, f) && rotations < p.ring.Len()-1 {
				rotations++
				attempt--
				continue
			}
		}
		// A rate limit widens the gap for later requests, so a long run
		// stops walking into the same wall; retrying only rescues this one.
		var rl *APIError
		if errors.As(err, &rl) && (rl.Status == 429 || rl.Status == 529) && !permanentLimit(rl.Error()) {
			p.pace.Refused()
		}
		if attempt >= p.cfg.MaxRetries || !retryable(err) || ctx.Err() != nil {
			return nil, wd.Wrap(err)
		}
		wait := time.Duration(500*(1<<attempt)) * time.Millisecond
		// The server's own Retry-After wins where it gave one: it knows
		// when its window resets, and the backoff is only a guess.
		var ra *APIError
		if errors.As(err, &ra) && ra.RetryAfter > 0 {
			wait = ra.RetryAfter
		}
		emit(llm.Event{Type: llm.EventStatus, Text: fmt.Sprintf("retrying %d/%d · %s · in %s", attempt+1, p.cfg.MaxRetries, retryReason(err), wait.Round(time.Second/10))})
		select {
		case <-ctx.Done():
			return nil, wd.Wrap(ctx.Err())
		case <-time.After(wait):
		}
	}
	defer resp.Body.Close()
	p.pace.Served()
	out, err := readStream(resp.Body, wd, emit)
	return out, wd.Wrap(err)
}

// applyDialect adjusts the request for quirks learned so far.
func (p *Provider) applyDialect(w wireRequest) wireRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.useMaxCompletion {
		w.MaxCompletionTokens, w.MaxTokens = w.MaxTokens, 0
	}
	if p.noStreamOptions {
		w.StreamOptions = nil
	}
	if p.noCacheKey {
		w.PromptCacheKey = ""
	}
	if p.noCacheControl {
		stripCacheControl(&w)
	}
	if p.noJSONSchema {
		// The caller parses the reply either way; without the constraint
		// it is asking rather than requiring, which is what it did before.
		w.ResponseFormat = nil
	}
	return w
}

// learnDialect inspects a 400 for a parameter complaint we can work around.
// It returns true when the request should be resent.
func (p *Provider) learnDialect(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 {
		return false
	}
	msg := strings.ToLower(ae.Message)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case !p.useMaxCompletion && strings.Contains(msg, "max_completion_tokens") && strings.Contains(msg, "max_tokens"):
		p.useMaxCompletion = true
		return true
	case !p.noStreamOptions && strings.Contains(msg, "stream_options"):
		p.noStreamOptions = true
		return true
	case !p.noCacheKey && strings.Contains(msg, "prompt_cache_key"):
		p.noCacheKey = true
		return true
	case !p.noCacheControl && (strings.Contains(msg, "cache_control") || strings.Contains(msg, "cachecontrol")):
		p.noCacheControl = true
		return true
	case !p.noJSONSchema && (strings.Contains(msg, "response_format") || strings.Contains(msg, "json_schema")):
		p.noJSONSchema = true
		return true
	}
	return false
}

func (p *Provider) do(ctx context.Context, body []byte, key string) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	if key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range extraHeaders(p.cfg.BaseURL) {
		hreq.Header.Set(k, v)
	}
	resp, err := p.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		msg := strings.TrimSpace(string(raw))
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		ae := &APIError{Status: resp.StatusCode, Message: msg}
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			ae.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
		}
		return nil, ae
	}
	return resp, nil
}

// maxRetryAfter is the longest the provider may ask the harness to wait.
const maxRetryAfter = 2 * time.Minute

// retryAfter reads a Retry-After header in either form the RFC allows,
// seconds or an HTTP date, and ignores anything longer than a couple of
// minutes: a server asking for an hour is telling the user to come back
// later, not telling the harness to sleep through it.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(v); err == nil {
		d = time.Until(when)
	}
	if d <= 0 || d > maxRetryAfter {
		return 0
	}
	return d
}

// exhausted are the ways a provider says the account has run out, rather
// than that this request arrived too fast. Both come back as 429, and
// retrying the first only delays a failure that was never going to clear —
// with the real reason buried under the retries.
var exhausted = []string{
	"insufficient_quota",
	"quota exceeded",
	"exceeded your current quota",
	"available balance",
	"insufficient balance",
	"out of budget",
	"billing",
	"usage limit reached",
	"credit balance is too low",
}

// permanentLimit reports whether a rate-limit response is really an
// exhausted account.
func permanentLimit(msg string) bool {
	msg = strings.ToLower(msg)
	for _, needle := range exhausted {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// retryReason names why a call is being retried, in the few words the
// status line has room for.
func retryReason(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 429:
			return "rate limited"
		case ae.Status >= 500:
			return fmt.Sprintf("server error %d", ae.Status)
		}
		return fmt.Sprintf("http %d", ae.Status)
	}
	return "connection failed"
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		if permanentLimit(ae.Error()) {
			return false
		}
		// 529 is Anthropic's "overloaded", which gateways pass through.
		// 500 and 501 are not retried: a plain internal error is the
		// server saying this request broke it, and sending it again
		// breaks it again — with the real reason buried under retries.
		switch ae.Status {
		case 429, 408, 529, 502, 503, 504:
			return true
		}
		return false
	}
	// Connection-level failures.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

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
