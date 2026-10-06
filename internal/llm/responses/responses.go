// Package responses is a provider for OpenAI's Responses API (/responses).
//
// Chat completions throws a reasoning model's reasoning away between
// calls: every tool result starts it thinking from scratch, which costs
// tokens and loses the plan it was following. The Responses API can hand
// that reasoning back, encrypted, so it is sent again with the next call.
// This provider asks for it (store: false, include
// reasoning.encrypted_content), keeps each reasoning item as a thinking
// block whose Signature is the encrypted payload, and replays it in
// place. Nothing is stored on the server; the history stays ours.
package responses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/httpstream"
)

// reasoningPrefix is how OpenAI's reasoning item ids begin. A thinking
// block from any other provider is never sent as one.
const reasoningPrefix = "rs_"

// Config configures the provider.
type Config struct {
	// BaseURL is the API root (default https://api.openai.com/v1); the
	// provider posts to BaseURL + "/responses".
	BaseURL string
	APIKeys []string
	// Name is the label shown to users (default "openai-responses").
	Name string
	// Thoughts asks for reasoning summaries, streamed as thinking.
	Thoughts   bool
	HTTPClient *http.Client
	MaxRetries int
	Limits     llm.StreamLimits
}

// Provider is a Responses API backend.
type Provider struct {
	cfg Config
	c   *httpstream.Client
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	ring := llm.NewKeyRing(cfg.APIKeys)
	if k, _ := ring.Current(); k == "" {
		return nil, llm.ErrNoAPIKey
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Name == "" {
		cfg.Name = "openai-responses"
	}
	w := &wire{url: cfg.BaseURL + "/responses", http: httpstream.HTTPClient(cfg.HTTPClient), thoughts: cfg.Thoughts}
	return &Provider{cfg: cfg, c: httpstream.New(w, ring, cfg.MaxRetries, cfg.Limits)}, nil
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return p.cfg.Name }

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	return p.c.Stream(ctx, req, emit)
}

// ForTask implements llm.PerTask.
func (p *Provider) ForTask() llm.Provider { return &Provider{cfg: p.cfg, c: p.c.ForTask()} }

type wire struct {
	url      string
	http     *http.Client
	thoughts bool

	mu sync.Mutex
	// noReasoning: the model refused the reasoning parameter (not a
	// reasoning model).
	noReasoning bool
	// noXHigh: the model refused effort "xhigh"; "high" is sent instead.
	noXHigh bool
	// noInclude: the server refused include (a gateway that does not
	// return encrypted reasoning).
	noInclude bool
	// noStore: the server refused store.
	noStore bool
	// noSchema: the server refused text.format.
	noSchema bool
}

func (w *wire) Send(ctx context.Context, req llm.Request, key string) (*http.Response, error) {
	w.mu.Lock()
	o := buildOpts{thoughts: w.thoughts, reasoning: !w.noReasoning, xhigh: !w.noXHigh, include: !w.noInclude, store: !w.noStore, schema: !w.noSchema}
	w.mu.Unlock()
	body, err := json.Marshal(buildRequest(req, o))
	if err != nil {
		return nil, err
	}
	return httpstream.Post(ctx, w.http, w.url, body, map[string]string{"Authorization": "Bearer " + key})
}

func (w *wire) Learn(err error) bool {
	var ae *httpstream.APIError
	if !errors.As(err, &ae) || ae.Status != 400 {
		return false
	}
	msg := strings.ToLower(ae.ErrorDetail())
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case !w.noXHigh && strings.Contains(msg, "xhigh"):
		w.noXHigh = true
		return true
	case !w.noReasoning && strings.Contains(msg, "reasoning") && !strings.Contains(msg, "encrypted"):
		w.noReasoning = true
		return true
	case !w.noInclude && (strings.Contains(msg, "include") || strings.Contains(msg, "encrypted_content")):
		w.noInclude = true
		return true
	case !w.noStore && strings.Contains(msg, "store"):
		w.noStore = true
		return true
	case !w.noSchema && (strings.Contains(msg, "text.format") || strings.Contains(msg, "json_schema")):
		w.noSchema = true
		return true
	}
	return false
}

func (w *wire) Read(body io.Reader, wd *llm.Watchdog, emit func(llm.Event)) (*llm.Response, error) {
	st := newStream(emit)
	err := httpstream.ReadSSE(body, wd, "responses", func(event string, data []byte) (bool, error) {
		return st.add(event, data)
	})
	if err != nil {
		return nil, err
	}
	return st.response(), nil
}
