// SPDX-License-Identifier: MIT
// Package openai implements llm.Provider over the OpenAI chat-completions
// wire format, which OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM and Ollama
// all speak. The client is hand-rolled so it stays tolerant of the small
// dialect differences between those servers.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
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
}

// Provider is an OpenAI-compatible chat backend.
type Provider struct {
	cfg  Config
	ring *llm.KeyRing
	http *http.Client

	// Dialect quirks learned from 400 responses, kept for the provider's
	// lifetime so each is paid for at most once.
	mu               sync.Mutex
	useMaxCompletion bool // send max_completion_tokens instead of max_tokens
	noStreamOptions  bool // server rejects stream_options
	noCacheControl   bool // server rejects cache_control / array content
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	ring := llm.NewKeyRing(append([]string{cfg.APIKey}, cfg.APIKeys...))
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
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
					tc := wireToolCall{ID: b.ID, Type: "function"}
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
	w, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
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
			// immediately without counting it as a retry.
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
		if attempt >= p.cfg.MaxRetries || !retryable(err) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(500*(1<<attempt)) * time.Millisecond):
		}
	}
	defer resp.Body.Close()
	return readStream(resp.Body, emit)
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
	if p.noCacheControl {
		stripCacheControl(&w)
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
	case !p.noCacheControl && (strings.Contains(msg, "cache_control") || strings.Contains(msg, "cachecontrol")):
		p.noCacheControl = true
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
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return resp, nil
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status == 408 || ae.Status >= 500
	}
	// Connection-level failures.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// toolAcc accumulates a streamed tool call.
type toolAcc struct {
	id, name string
	args     strings.Builder
	started  bool
}

func readStream(r io.Reader, emit func(llm.Event)) (*llm.Response, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var (
		text     strings.Builder
		thinking strings.Builder
		tools    = map[int]*toolAcc{}
		order    []int
		finish   string
		usage    llm.Usage
		gotDone  bool
		gotChunk bool
	)
	// Blocks are emitted in the order: thinking, text, tool calls, matching how
	// the model produced them well enough for history replay.
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			gotDone = true
			break
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
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc, ok := tools[idx]
				if !ok {
					acc = &toolAcc{}
					tools[idx] = acc
					order = append(order, idx)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name += tc.Function.Name
				}
				if !acc.started && acc.name != "" {
					acc.started = true
					if acc.id == "" {
						acc.id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), idx)
					}
					emit(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{Type: llm.BlockToolUse, ID: acc.id, Name: acc.name}})
				}
				if tc.Function.Arguments != "" {
					acc.args.WriteString(tc.Function.Arguments)
					emit(llm.Event{Type: llm.EventToolUseDelta, Text: tc.Function.Arguments})
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
	sort.Ints(order)
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
