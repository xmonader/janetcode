package main

import (
	"context"
	"errors"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

type hedgeWireFake struct{ name string }

func (f hedgeWireFake) Name() string { return f.name }
func (f hedgeWireFake) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	return nil, errors.New("unused")
}

func hedgeWireBare(cfg *config.Config) (llm.Provider, error) {
	return hedgeWireFake{name: cfg.Provider}, nil
}

func hedgeWireCfg() *config.Config {
	return &config.Config{Provider: "main", Providers: map[string]config.Provider{
		"main":   {Type: "anthropic"},
		"second": {Type: "anthropic"},
		"ds":     {Type: config.TypeOpenAI, BaseURL: "https://api.deepseek.com/beta"},
		"broker": {Type: config.TypeOpenAI, BaseURL: "https://ai02.example/v1"},
	}}
}

type hedgeWireStats interface{ HedgeStats() llm.HedgeStats }
type resumeWireStats interface{ ResumeStats() llm.ResumeStats }

// Nothing set: the bare provider, unwrapped.
func TestWrapRouteOffByDefault(t *testing.T) {
	p, err := wrapRoute(hedgeWireCfg(), hedgeWireBare)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(hedgeWireFake); !ok {
		t.Fatalf("got %T, want the bare provider", p)
	}
}

func TestWrapRouteHedgeAndResume(t *testing.T) {
	cfg := hedgeWireCfg()
	cfg.HedgeProvider, cfg.StreamResume = "second", true
	p, err := wrapRoute(cfg, hedgeWireBare)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(resumeWireStats); !ok {
		t.Fatalf("got %T, want resume outermost", p)
	}
	if p.Name() != "main" {
		t.Fatalf("name = %q", p.Name())
	}
	cfg.StreamResume = false
	p, _ = wrapRoute(cfg, hedgeWireBare)
	if _, ok := p.(hedgeWireStats); !ok {
		t.Fatalf("got %T, want a hedge", p)
	}
}

// A hedge provider that is not configured is a config error, not a
// silently unhedged session.
func TestWrapRouteUnknownHedge(t *testing.T) {
	cfg := hedgeWireCfg()
	cfg.HedgeProvider = "nope"
	if _, err := wrapRoute(cfg, hedgeWireBare); !errors.Is(err, config.ErrBadConfig) {
		t.Fatalf("err = %v", err)
	}
}

// Resume only where the provider continues a reply: an OpenAI-compatible
// server that is not DeepSeek's beta endpoint would answer after the
// partial, and the stitched reply would repeat itself.
func TestWrapRouteResumeOnlyWhereSupported(t *testing.T) {
	for _, tc := range []struct {
		provider, hedge string
		want            bool
	}{
		{"main", "", true},
		{"ds", "", true},
		{"broker", "", false},
		{"main", "broker", false},
	} {
		cfg := hedgeWireCfg()
		cfg.Provider, cfg.HedgeProvider, cfg.StreamResume = tc.provider, tc.hedge, true
		p, err := wrapRoute(cfg, hedgeWireBare)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(resumeWireStats); ok != tc.want {
			t.Errorf("%s+%s: resume = %v, want %v", tc.provider, tc.hedge, ok, tc.want)
		}
	}
	if resumeCapable(config.Provider{Type: config.TypeOpenAI, BaseURL: "https://api.deepseek.com/v1"}) {
		t.Error("DeepSeek's non-beta endpoint has no prefix completion")
	}
	if resumeCapable(config.Provider{Type: config.TypeOpenAI, BaseURL: "https://evil.example/beta?deepseek.com"}) {
		t.Error("a host merely mentioning deepseek.com was taken for it")
	}
}
