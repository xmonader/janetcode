// Package opencode routes OpenCode Go and Zen models through the existing
// protocol clients, keeping gateway headers consistent on every API path.
package opencode

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/gemini"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/llm/responses"
)

type Config struct {
	Name         string
	BaseURL      string
	APIKeys      []string
	HTTPClient   *http.Client
	Thinking     bool
	HideThinking bool
	Effort       string
	MaxRetries   int
	Limits       llm.StreamLimits
}

type Provider struct {
	cfg     Config
	session string
	mu      sync.Mutex
	clients map[string]llm.Provider
	learned map[string]string
	models  *openai.Provider
}

func New(cfg Config) (*Provider, error) {
	if cfg.Name != "opencode-go" && cfg.Name != "opencode-zen" {
		return nil, fmt.Errorf("unknown OpenCode provider %q", cfg.Name)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://opencode.ai/inference/v1"
		if cfg.Name == "opencode-go" {
			cfg.BaseURL = "https://opencode.ai/inference/go/v1"
		}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if strings.Contains(cfg.BaseURL, "/inference/") {
		for _, suffix := range []string{"/openai/v1", "/anthropic/v1", "/anthropic", "/google/v1beta"} {
			if strings.HasSuffix(cfg.BaseURL, suffix) {
				cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, suffix) + "/v1"
				break
			}
		}
	}
	p := &Provider{cfg: cfg, session: rand.Text(), clients: map[string]llm.Provider{}, learned: map[string]string{}}
	models, err := openai.New(openai.Config{BaseURL: cfg.BaseURL, APIKeys: cfg.APIKeys, Name: cfg.Name, HTTPClient: p.httpClient(), MaxRetries: cfg.MaxRetries, Limits: cfg.Limits})
	if err != nil {
		return nil, err
	}
	p.models = models
	return p, nil
}

func (p *Provider) Name() string         { return p.cfg.Name }
func (p *Provider) SupportsEffort() bool { return true }
func (p *Provider) ForTask() llm.Provider {
	// Models share credentials but each standalone task gets a distinct
	// fallback session header. Agent contexts override it on every call.
	child, err := New(p.cfg)
	if err != nil {
		return p
	} // retain the usable provider if a future config field cannot be cloned
	return child
}

func protocol(name, model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "jev-"):
		return "unsupported"
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "grok-"), strings.HasPrefix(m, "muse-spark-"):
		return "responses"
	case strings.HasPrefix(m, "claude-"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini-"):
		return "gemini"
	}
	if name == "opencode-go" {
		if m == "qwen3.8-max" || m == "qwen3.8-flash" || m == "qwen3.7-plus" || strings.HasPrefix(m, "minimax-") && m != "minimax-m3.1-flash" {
			return "anthropic"
		}
	} else if m == "qwen3.8-flash" || strings.HasPrefix(m, "qwen3.7-") || strings.HasPrefix(m, "qwen3.6-") || strings.HasPrefix(m, "qwen3.5-") {
		return "anthropic"
	}
	return "openai"
}

// Modern gateways expose separate native API bases. Legacy /zen/.../v1
// URLs keep the single-base behavior used by qwengo and older configs.
func (p *Provider) apiBase(proto string) string {
	base := p.cfg.BaseURL
	if strings.Contains(base, "/inference/") && strings.HasSuffix(base, "/v1") {
		root := strings.TrimSuffix(base, "/v1")
		switch proto {
		case "anthropic":
			return root + "/anthropic"
		case "gemini":
			return root + "/google/v1beta"
		default:
			return root + "/openai/v1"
		}
	}
	if proto == "anthropic" {
		return strings.TrimSuffix(base, "/v1")
	}
	return base
}

func (p *Provider) client(proto string) (llm.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[proto]; ok {
		return c, nil
	}
	var c llm.Provider
	var err error
	base, hc, cfg := p.apiBase(proto), p.httpClient(), p.cfg
	switch proto {
	case "openai":
		c, err = openai.New(openai.Config{BaseURL: base, APIKeys: cfg.APIKeys, Name: cfg.Name, HTTPClient: hc, MaxRetries: cfg.MaxRetries, Limits: cfg.Limits})
	case "responses":
		c, err = responses.New(responses.Config{BaseURL: base, APIKeys: cfg.APIKeys, Name: cfg.Name, HTTPClient: hc, Thoughts: !cfg.HideThinking, MaxRetries: cfg.MaxRetries, Limits: cfg.Limits})
	case "anthropic":
		c, err = anthropic.New(anthropic.Config{BaseURL: base, APIKeys: cfg.APIKeys, HTTPClient: hc, Thinking: cfg.Thinking, HideThinking: cfg.HideThinking, Effort: cfg.Effort, MaxRetries: cfg.MaxRetries, Limits: cfg.Limits})
	case "gemini":
		c, err = gemini.New(gemini.Config{BaseURL: base, APIKeys: cfg.APIKeys, Name: cfg.Name, HTTPClient: hc, Thoughts: !cfg.HideThinking, MaxRetries: cfg.MaxRetries, Limits: cfg.Limits})
	default:
		err = fmt.Errorf("%s model uses an unsupported proprietary endpoint; choose another model", cfg.Name)
	}
	if err == nil {
		p.clients[proto] = c
	}
	return c, err
}

func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if prefix, model, ok := strings.Cut(req.Model, "/"); ok && prefix == p.cfg.Name {
		req.Model = model
	}
	proto := protocol(p.cfg.Name, req.Model)
	p.mu.Lock()
	if learned := p.learned[req.Model]; learned != "" {
		proto = learned
	}
	p.mu.Unlock()
	client, err := p.client(proto)
	if err != nil {
		return nil, err
	}
	produced := false
	forward := func(e llm.Event) {
		if e.Type == llm.EventTextDelta || e.Type == llm.EventThinkingDelta || e.Type == llm.EventToolUseStart || e.Type == llm.EventToolUseDelta {
			produced = true
		}
		if emit != nil {
			emit(e)
		}
	}
	ctx = context.WithValue(ctx, modelKey{}, req.Model)
	resp, err := client.Stream(ctx, req, forward)
	// Correct a stale table once, only before any model output. Auth,
	// quota and transient service failures stay on their original API.
	var status llm.StatusError
	if !produced && ctx.Err() == nil && errors.As(err, &status) {
		text := strings.ToLower(llm.ClassifyText(err))
		if status.HTTPStatus() == 404 || status.HTTPStatus() == 405 || status.HTTPStatus() == 400 && strings.Contains(text, "endpoint") {
			next := "openai"
			if proto == next || strings.Contains(text, "responses") {
				next = "responses"
			}
			if strings.Contains(text, "messages") {
				next = "anthropic"
			}
			if next != proto {
				other, buildErr := p.client(next)
				if buildErr == nil {
					if emit != nil {
						emit(llm.Event{Type: llm.EventStreamDiscard})
					}
					resp, err = other.Stream(ctx, req, forward)
					if err == nil {
						p.mu.Lock()
						p.learned[req.Model] = next
						p.mu.Unlock()
					}
				}
			}
		}
	}
	return resp, err
}

func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := p.models.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := models[:0]
	for _, m := range models {
		if protocol(p.cfg.Name, m.ID) != "unsupported" {
			out = append(out, m)
		}
	}
	return out, nil
}
