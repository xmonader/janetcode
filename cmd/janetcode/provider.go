package main

import (
	"fmt"
	"sync"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/gemini"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/llm/responses"
)

// buildProvider constructs the backend selected by cfg.Provider.
// providerPool builds one backend per configured provider and keeps it,
// so a session can send work to more than one model without rebuilding a
// client for every call.
type providerPool struct {
	cfg *config.Config
	mu  sync.Mutex
	by  map[string]llm.Provider
}

func newProviderPool(cfg *config.Config, active llm.Provider) *providerPool {
	p := &providerPool{cfg: cfg, by: map[string]llm.Provider{}}
	if active != nil && cfg != nil && cfg.Provider != "" {
		p.by[cfg.Provider] = active
	}
	return p
}

// For resolves a model name to the backend that serves it. The name is
// returned as well, so the caller records what actually ran.
func (p *providerPool) For(model string) (llm.Provider, string, error) {
	if p == nil || p.cfg == nil {
		return nil, "", fmt.Errorf("no providers are configured")
	}
	name, ok := p.cfg.RouteFor(model)
	if !ok {
		return nil, "", fmt.Errorf("no configured provider serves %q; add one with /connect, or route it in the config", model)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prov, ok := p.by[name]; ok {
		return prov, model, nil
	}
	// Build the named provider by copying the config with it selected.
	sub := *p.cfg
	sub.Provider = name
	prov, err := buildProvider(&sub)
	if err != nil {
		return nil, "", fmt.Errorf("%s (for model %s): %w", name, model, err)
	}
	p.by[name] = prov
	return prov, model, nil
}

func buildProvider(cfg *config.Config) (llm.Provider, error) {
	return wrapRoute(cfg, buildBareProvider)
}

func buildBareProvider(cfg *config.Config) (llm.Provider, error) {
	p := cfg.Active()
	switch p.Type {
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKeys:      p.Keys(),
			BaseURL:      p.BaseURL,
			Bearer:       p.Bearer(),
			Thinking:     cfg.ThinkingEnabled(),
			HideThinking: !cfg.ThinkingShown(),
			Effort:       cfg.Effort,
			Limits:       cfg.StreamLimits(),
		})
	case config.TypeOpenAI:
		return openai.New(openai.Config{
			APIKeys: p.Keys(),
			BaseURL: p.BaseURL,
			Name:    cfg.Provider,
			Limits:  cfg.StreamLimits(),
		})
	case config.TypeGemini:
		return gemini.New(gemini.Config{
			APIKeys:  p.Keys(),
			BaseURL:  p.BaseURL,
			Name:     cfg.Provider,
			Thoughts: cfg.ThinkingShown(),
			Limits:   cfg.StreamLimits(),
		})
	case config.TypeResponses:
		return responses.New(responses.Config{
			APIKeys:  p.Keys(),
			BaseURL:  p.BaseURL,
			Name:     cfg.Provider,
			Thoughts: cfg.ThinkingShown(),
			Limits:   cfg.StreamLimits(),
		})
	}
	return nil, fmt.Errorf("%w: provider %q has unknown type %q", config.ErrBadConfig, cfg.Provider, p.Type)
}
