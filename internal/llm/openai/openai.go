// Package openai implements llm.Provider over the OpenAI chat-completions
// wire format, which OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM and Ollama
// all speak. The client is hand-rolled so it stays tolerant of the small
// dialect differences between those servers.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// streamed. Zero means the default of 2; negative means no retries at
	// all, so that "do not retry" is expressible and does not read as
	// "use the default" the way a plain 0 did.
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
	// refusing them, so a long run stops walking into the same limit. It
	// is per provider value, not shared with the siblings ForTask hands
	// out, so one unit of work being refused does not slow the others.
	pace *llm.Pacer

	// dialect is shared with every sibling: a quirk is a property of the
	// server, so it is learned — and paid for — at most once.
	dialect *dialect
}

// dialect is what a server told us about itself through its 400s, kept
// for the lifetime of the provider and everything ForTask derives from it.
type dialect struct {
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
	switch {
	case cfg.MaxRetries == 0:
		cfg.MaxRetries = 2
	case cfg.MaxRetries < 0:
		cfg.MaxRetries = 0
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
	return &Provider{cfg: cfg, ring: ring, http: hc, pace: &llm.Pacer{}, dialect: &dialect{}}, nil
}

// ForTask returns a provider for one unit of concurrent work: the same
// endpoint, the same key ring and the same learned dialect, but a pacer of
// its own.
//
// A pacer is shared state that only ever widens, so one shared across
// concurrent tasks turns a single 429 on one of them into a gap in front
// of every request the others make — the eval driver, which runs its tasks
// in one process, was measurably slower than the same tasks in separate
// processes for no reason but that. The key ring has to stay shared: it is
// where a burnt key is remembered, and that is worth knowing everywhere.
func (p *Provider) ForTask() llm.Provider {
	return &Provider{cfg: p.cfg, ring: p.ring, http: p.http, pace: &llm.Pacer{}, dialect: p.dialect}
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
	wd.Expect(p.pace.Gap())
	if err := p.pace.Wait(ctx); err != nil {
		// A bound that fired while the call was waiting its turn is the
		// reason it was cancelled; without this the user sees a bare
		// "context canceled".
		return nil, wd.Wrap(err)
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
	// One retry policy, shared with every other provider (llm.Retrier):
	// it decides what is worth another attempt, waits out the backoff or
	// the server's own Retry-After, keeps the watchdog informed, and
	// reports both the countdown and, once the call lands, what it had to
	// survive.
	rt := llm.NewRetrier(p.cfg.MaxRetries, emit, wd)
	for {
		body, err := json.Marshal(p.applyDialect(w))
		if err != nil {
			return nil, err
		}
		key, idx := p.ring.Current()
		resp, err = p.do(ctx, body, key)
		if err == nil {
			p.ring.OK(idx)
			rt.Landed()
			break
		}
		if p.learnDialect(err) {
			// A parameter the server rejects was dropped or renamed;
			// resend immediately. Working out the dialect must not spend
			// the budget meant for transient failures, and it does not:
			// only a failure the Retrier is asked about counts.
			continue
		}
		if f, ok := keyProblem(err); ok {
			// Remember the bad key; another may have quota left, so try it
			// right away without counting a retry.
			if p.ring.Fail(idx, f) && rotations < p.ring.Len()-1 {
				rotations++
				continue
			}
		}
		// A rate limit widens the gap for later requests, so a long run
		// stops walking into the same wall; retrying only rescues this one.
		var rl *APIError
		if errors.As(err, &rl) && (rl.Status == 429 || rl.Status == 529) && !llm.PermanentLimit(rl.Error()) {
			p.pace.Refused()
		}
		if werr := rt.Wait(ctx, err); werr != nil {
			return nil, wd.Wrap(werr)
		}
	}
	defer resp.Body.Close()
	p.pace.Served()
	out, err := readStream(resp.Body, wd, emit)
	return out, wd.Wrap(err)
}

// applyDialect adjusts the request for quirks learned so far.
func (p *Provider) applyDialect(w wireRequest) wireRequest {
	p.dialect.mu.Lock()
	defer p.dialect.mu.Unlock()
	if p.dialect.useMaxCompletion {
		w.MaxCompletionTokens, w.MaxTokens = w.MaxTokens, 0
	}
	if p.dialect.noStreamOptions {
		w.StreamOptions = nil
	}
	if p.dialect.noCacheKey {
		w.PromptCacheKey = ""
	}
	if p.dialect.noCacheControl {
		stripCacheControl(&w)
	}
	if p.dialect.noJSONSchema {
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
	p.dialect.mu.Lock()
	defer p.dialect.mu.Unlock()
	switch {
	case !p.dialect.useMaxCompletion && strings.Contains(msg, "max_completion_tokens") && strings.Contains(msg, "max_tokens"):
		p.dialect.useMaxCompletion = true
		return true
	case !p.dialect.noStreamOptions && strings.Contains(msg, "stream_options"):
		p.dialect.noStreamOptions = true
		return true
	case !p.dialect.noCacheKey && strings.Contains(msg, "prompt_cache_key"):
		p.dialect.noCacheKey = true
		return true
	case !p.dialect.noCacheControl && (strings.Contains(msg, "cache_control") || strings.Contains(msg, "cachecontrol")):
		p.dialect.noCacheControl = true
		return true
	case !p.dialect.noJSONSchema && (strings.Contains(msg, "response_format") || strings.Contains(msg, "json_schema")):
		p.dialect.noJSONSchema = true
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
		return nil, &APIError{
			Status:     resp.StatusCode,
			Message:    msg,
			RetryAfter: llm.RetryAfterFrom(resp.Header, resp.StatusCode),
		}
	}
	return resp, nil
}
