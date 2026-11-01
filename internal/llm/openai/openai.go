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
	"regexp"
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
	// maxOut is the largest output limit the server accepts, learned from a
	// 400 that refused a larger one; 0 until then. lastMax is the limit the
	// last request asked for, the fallback when the refusal names none.
	maxOut, lastMax int
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
		// A clone of the default transport, not a bare one: it keeps the
		// dial and TLS handshake timeouts, HTTP/2, the proxy variables
		// and any certificate authorities netcfg added (a bare
		// Transport had none of the last, so a TLS-intercepting proxy's
		// certificate was refused here and nowhere else).
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = 2 * time.Minute
		hc = &http.Client{Timeout: RequestTimeout, Transport: tr}
	}
	// Both request paths — the stream and ListModels — share this client,
	// and both send the key. Without a policy Go forwards Authorization
	// to any subdomain of the endpoint's host, never checks the scheme,
	// and copies extraHeaders anywhere at all.
	hc = llm.SafeRedirectClient(hc)
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
	// The uncapped copy: a burnt account whose "insufficient_quota" sits
	// past the display cap must still read as a quota failure, or its key
	// gets a 15-second breaker and is tried again forever.
	full := ae.ErrorDetail()
	msg := strings.ToLower(full)
	switch {
	case ae.Status == 429 || ae.Status == 401 || ae.Status == 403 || ae.Status == 402:
	case strings.Contains(msg, "quota") || strings.Contains(msg, "rate limit"):
	default:
		return 0, false
	}
	return llm.ClassifyFailure(ae.Status, full), true
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
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyRead))
	if resp.StatusCode/100 != 2 {
		msg := llm.SafeErrorMessage(resp.StatusCode, strings.TrimSpace(string(raw)))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			if m := llm.SafeErrorMessage(resp.StatusCode, env.Error.Message); m != "" {
				msg = m
			}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	if readErr != nil {
		return nil, fmt.Errorf("embeddings: read response: %w", readErr)
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
	seen := make([]bool, len(texts))
	for _, d := range out.Data {
		idx := d.Index
		if idx < 0 || idx >= len(texts) {
			return nil, fmt.Errorf("embeddings: vector index %d out of range for %d inputs", idx, len(texts))
		}
		if seen[idx] {
			return nil, fmt.Errorf("embeddings: duplicate vector index %d", idx)
		}
		seen[idx] = true
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
	// The caller's context is the user's will: a cancellation there ends
	// the call for good. Each attempt gets a watchdog of its own, so a
	// stream the harness killed for silence can be retried under a fresh
	// one — a stream that died without a terminal frame never answered.
	parent := ctx
	var wd *llm.Watchdog
	// Spacing imposed by earlier rate limits, if any. Costs nothing on a
	// session the provider has never refused.
	pendingGap := p.pace.Gap()

	var resp *http.Response
	rotations := 0
	// One retry policy, shared with every other provider (llm.Retrier):
	// it decides what is worth another attempt, waits out the backoff or
	// the server's own Retry-After, keeps the watchdog informed, and
	// reports both the countdown and, once the call lands, what it had to
	// survive.
	rt := llm.NewRetrier(p.cfg.MaxRetries, emit, nil)
	// One lifetime budget for the whole call, not one per attempt: the
	// cap exists to end a stream that never stops dribbling, and a
	// per-attempt one multiplied it by the retry count.
	callStart := time.Now()
attempts:
	for {
		ctx, wd = llm.WatchSince(parent, p.cfg.Limits, callStart)
		rt.ResetWatchdog(wd)
		wd.Expect(pendingGap)
		if err := p.pace.Wait(ctx); err != nil {
			// A bound that fired while the call was waiting its turn is the
			// reason it was cancelled; without this the user sees a bare
			// "context canceled".
			wd.Stop()
			return nil, wd.Wrap(err)
		}

		w, err := buildRequest(req)
		if err != nil {
			wd.Stop()
			return nil, err
		}
		w.PromptCacheKey = p.cfg.CacheKey
		if cacheableURL(p.cfg.BaseURL) {
			applyCacheBreakpoints(&w)
		}
		for {
			body, err := json.Marshal(p.applyDialect(w))
			if err != nil {
				wd.Stop()
				return nil, err
			}
			key, idx := p.ring.Current()
			resp, err = p.do(ctx, body, key)
			if err == nil {
				p.ring.OK(idx)
				rt.Landed()
				break
			}
			if stall := wd.Err(); stall != nil && parent.Err() == nil {
				// The watchdog killed the wait for headers. The transport
				// reports that as a bare context cancellation, which the
				// retry policy reads as the user giving up, and this
				// attempt's context is dead besides — so the stall is
				// judged as itself, and a retry gets a fresh watchdog
				// exactly as a stall mid-stream does.
				wd.Stop()
				if llm.LifetimeStalled(stall) {
					return nil, stall
				}
				if werr := rt.Wait(parent, stall); werr != nil {
					return nil, werr
				}
				continue attempts
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
			// ClassifyText, not Error(): the display copy is cut, and an
			// exhausted account whose quota word sits past the cut is not
			// a limit that pacing later requests will ever get under.
			if errors.As(err, &rl) && (rl.Status == 429 || rl.Status == 529) && !llm.PermanentLimit(llm.ClassifyText(rl)) {
				p.pace.Refused()
				pendingGap = p.pace.Gap()
			}
			if werr := rt.Wait(parent, err); werr != nil {
				wd.Stop()
				return nil, wd.Wrap(werr)
			}
		}
		p.pace.Served()
		out, err := readStream(resp.Body, wd, emit)
		resp.Body.Close()
		if err == nil {
			wd.Stop()
			return out, nil
		}
		// A stream that ended without a terminal frame — connection
		// dropped, body ended early, chunk malformed, or the watchdog
		// killed the silence — never produced an answer, so the whole
		// request is retried as one fresh attempt over the intact
		// history. The partial deltas were only ever events; the retried
		// response replaces them. Anything else keeps the old rule: a
		// failure past real output is final, and the user resends.
		stalled := wd.Err() != nil
		if stalled {
			err = wd.Wrap(err) // the watchdog's reason, not bare ctx cancel
		}
		wd.Stop()
		if parent.Err() != nil {
			return nil, wd.Wrap(err)
		}
		// The lifetime cap is the bound on this call. It has not died —
		// it is out of time, and every retry spends the same exhausted
		// budget, so it ends here.
		if llm.LifetimeStalled(wd.Err()) {
			return nil, wd.Wrap(err)
		}
		if llm.Truncated(err) || stalled {
			emit(llm.Event{Type: llm.EventStreamDiscard})
			if werr := rt.Wait(parent, err); werr != nil {
				return nil, wd.Wrap(werr)
			}
			continue
		}
		return nil, wd.Wrap(err)
	}
}

// applyDialect adjusts the request for quirks learned so far.
func (p *Provider) applyDialect(w wireRequest) wireRequest {
	p.dialect.mu.Lock()
	defer p.dialect.mu.Unlock()
	if p.dialect.maxOut > 0 && w.MaxTokens > p.dialect.maxOut {
		w.MaxTokens = p.dialect.maxOut
	}
	p.dialect.lastMax = w.MaxTokens
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
	if ae.Detail != "" {
		msg += " " + strings.ToLower(ae.Detail)
	}
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
	case p.dialect.lastMax > 1024 && outputLimitRefusal(msg):
		// The output limit asked for is more than this model serves. The
		// largest number the refusal names below what was asked is taken
		// as the server's limit; with none, half is tried.
		limit := 0
		for _, m := range numberRe.FindAllString(msg, -1) {
			if n, err := strconv.Atoi(m); err == nil && n >= 1024 && n < p.dialect.lastMax && n > limit {
				limit = n
			}
		}
		if limit == 0 {
			limit = p.dialect.lastMax / 2
		}
		p.dialect.maxOut = limit
		return true
	}
	return false
}

var numberRe = regexp.MustCompile(`\d{4,7}`)

// outputLimitRefusal reports whether a 400's message is about the output
// token limit being too large.
func outputLimitRefusal(msg string) bool {
	if !strings.Contains(msg, "max_tokens") && !strings.Contains(msg, "max_completion_tokens") && !strings.Contains(msg, "max_output_tokens") && !strings.Contains(msg, "maximum output") {
		return false
	}
	for _, w := range []string{"too large", "exceed", "must be less", "less than or equal", "at most", "maximum", "<=", "range", "greater than"} {
		if strings.Contains(msg, w) {
			return true
		}
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
		detail := llm.SafeErrorDetail(resp.StatusCode, msg)
		msg = llm.SafeErrorMessage(resp.StatusCode, msg)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, &APIError{
			Status:     resp.StatusCode,
			Message:    msg,
			Detail:     detail,
			RetryAfter: llm.RetryAfterFrom(resp.Header, resp.StatusCode),
		}
	}
	return resp, nil
}
