// Package gemini is a provider for Google's Gemini API, spoken natively
// (generateContent over SSE) rather than through its OpenAI-compatible
// endpoint. The compatible endpoint drops the one thing a multi-turn tool
// loop on Gemini 3 cannot do without: the thoughtSignature the model puts
// on its function calls, which must come back verbatim on the next
// request or the request is refused with a 400. Here the signature is
// kept on the block it arrived on and sent back with it.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/httpstream"
)

// DefaultBaseURL is the public Gemini API.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// skipSignature is Google's documented stand-in for a function call that
// has no signature of its own -- one made by another model earlier in
// the session. It costs the model its reasoning context for that call,
// which is still better than a request refused outright.
const skipSignature = "skip_thought_signature_validator"

// callPrefix marks a call id the harness made up because the model sent
// none; it is never sent back as an id the API would not recognise.
const callPrefix = "gemini_call_"

// Config configures the provider.
type Config struct {
	BaseURL string
	APIKeys []string
	// Name is the label shown to users (default "gemini").
	Name string
	// Thoughts asks for thought summaries, streamed as thinking.
	Thoughts bool
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
	MaxRetries int
	Limits     llm.StreamLimits
}

// Provider is a Gemini backend.
type Provider struct {
	cfg  Config
	wire *wire
	c    *httpstream.Client
}

// New validates cfg and builds a provider.
func New(cfg Config) (*Provider, error) {
	ring := llm.NewKeyRing(cfg.APIKeys)
	if k, _ := ring.Current(); k == "" {
		return nil, llm.ErrNoAPIKey
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Name == "" {
		cfg.Name = "gemini"
	}
	w := &wire{base: cfg.BaseURL, http: httpstream.HTTPClient(cfg.HTTPClient), thoughts: cfg.Thoughts, sentinel: true}
	return &Provider{cfg: cfg, wire: w, c: httpstream.New(w, ring, cfg.MaxRetries, cfg.Limits)}, nil
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return p.cfg.Name }

// SupportsEffort implements llm.EffortAware: the tier is sent as the
// thinking level (until a model refuses one; see wire.Learn). It does
// not implement llm.EffortCacheNeutral: whether the implicit cache keys
// on the thinking config is not documented, and the safe guess is that
// it does.
func (p *Provider) SupportsEffort() bool { return true }

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	return p.c.Stream(ctx, req, emit)
}

// ForTask implements llm.PerTask.
func (p *Provider) ForTask() llm.Provider {
	return &Provider{cfg: p.cfg, wire: p.wire, c: p.c.ForTask()}
}

// ListModels implements llm.ModelLister: the models that can generate
// content, with their limits.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	key, _ := p.c.Ring.Current()
	var out []llm.ModelInfo
	token := ""
	for page := 0; page < 20; page++ {
		u := p.cfg.BaseURL + "/models?pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		hreq.Header.Set("x-goog-api-key", key)
		resp, err := p.wire.http.Do(hreq)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			msg := llm.SafeErrorMessage(resp.StatusCode, httpstream.ErrorMessage(raw))
			if msg == "" {
				msg = http.StatusText(resp.StatusCode)
			}
			return nil, &httpstream.APIError{Status: resp.StatusCode, Message: msg}
		}
		var body struct {
			Models []struct {
				Name             string   `json:"name"`
				DisplayName      string   `json:"displayName"`
				InputTokenLimit  int      `json:"inputTokenLimit"`
				OutputTokenLimit int      `json:"outputTokenLimit"`
				Methods          []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("gemini: model list: %w", err)
		}
		for _, m := range body.Models {
			if !hasMethod(m.Methods, "generateContent") {
				continue
			}
			out = append(out, llm.ModelInfo{ID: strings.TrimPrefix(m.Name, "models/"), Name: m.DisplayName, ContextWindow: m.InputTokenLimit, MaxOutput: m.OutputTokenLimit})
		}
		if token = body.NextPageToken; token == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func hasMethod(ms []string, want string) bool {
	for _, m := range ms {
		if m == want {
			return true
		}
	}
	return false
}

// wire is the Gemini protocol, with what the server has taught us.
type wire struct {
	base     string
	http     *http.Client
	thoughts bool

	mu sync.Mutex
	// noLevel: the model refused thinkingLevel (a 2.x model).
	noLevel bool
	// noThinking: the model refused thinkingConfig altogether.
	noThinking bool
	// noSchema: the model refused responseJsonSchema.
	noSchema bool
	// sentinel: unsigned calls carry skipSignature. Off once a model
	// refuses the stand-in.
	sentinel bool
}

func (w *wire) Send(ctx context.Context, req llm.Request, key string) (*http.Response, error) {
	w.mu.Lock()
	opts := buildOpts{thoughts: w.thoughts, level: !w.noLevel, thinking: !w.noThinking, schema: !w.noSchema, sentinel: w.sentinel}
	w.mu.Unlock()
	body, err := json.Marshal(buildRequest(req, opts))
	if err != nil {
		return nil, err
	}
	model := strings.TrimPrefix(req.Model, "models/")
	u := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", w.base, url.PathEscape(model))
	return httpstream.Post(ctx, w.http, u, body, map[string]string{"x-goog-api-key": key})
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
	case !w.noLevel && (strings.Contains(msg, "thinking_level") || strings.Contains(msg, "thinkinglevel") || strings.Contains(msg, "thinking level")):
		w.noLevel = true
		return true
	case !w.noThinking && (strings.Contains(msg, "thinking_config") || strings.Contains(msg, "thinkingconfig") || strings.Contains(msg, "thinking is not supported") || strings.Contains(msg, "include_thoughts")):
		w.noThinking = true
		return true
	case !w.noSchema && (strings.Contains(msg, "response_json_schema") || strings.Contains(msg, "responsejsonschema") || strings.Contains(msg, "response_mime_type")):
		w.noSchema = true
		return true
	case w.sentinel && strings.Contains(msg, "signature") && !strings.Contains(msg, "missing"):
		w.sentinel = false
		return true
	}
	return false
}

func (w *wire) Read(body io.Reader, wd *llm.Watchdog, emit func(llm.Event)) (*llm.Response, error) {
	st := &stream{emit: emit}
	err := httpstream.ReadSSE(body, wd, "gemini", func(_ string, data []byte) (bool, error) {
		return false, st.add(data)
	})
	// No frame is marked terminal: the stream is complete when the body
	// ends after a finish reason (or a blocked prompt).
	if errors.Is(err, httpstream.ErrEndedEarly) && st.finish != "" {
		err = nil
	}
	if err != nil {
		if errors.Is(err, httpstream.ErrEndedEarly) && st.frames > 0 {
			return nil, llm.Truncate(errors.New("gemini: stream ended without a finish reason"))
		}
		return nil, err
	}
	return st.response(), nil
}
