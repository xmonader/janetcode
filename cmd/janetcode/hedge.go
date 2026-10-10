package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/xdg"
)

// wrapRoute builds the configured provider with bare and wraps it in the
// opt-in route mechanisms: hedging onto hedge_provider, and resuming a
// broken stream (stream_resume) where the provider can continue a reply.
// With neither set it is bare(cfg).
func wrapRoute(cfg *config.Config, bare func(*config.Config) (llm.Provider, error)) (llm.Provider, error) {
	p, err := bare(cfg)
	if err != nil || cfg == nil {
		return p, err
	}
	resumable := resumeCapable(cfg.Active())
	if name := cfg.HedgeProvider; name != "" && name != cfg.Provider {
		if _, ok := cfg.Providers[name]; !ok {
			return nil, fmt.Errorf("%w: hedge_provider %q is not a configured provider", config.ErrBadConfig, name)
		}
		sub := *cfg
		sub.Provider = name
		second, err := bare(&sub)
		if err != nil {
			return nil, fmt.Errorf("hedge_provider %s: %w", name, err)
		}
		p = llm.Hedge(p, second, llm.HedgeConfig{After: time.Duration(cfg.HedgeAfterMs) * time.Millisecond})
		// A continuation can land on either route, so both must be able
		// to continue one.
		resumable = resumable && resumeCapable(cfg.Providers[name])
	}
	if cfg.StreamResume && resumable {
		// DeepSeek's prefix completion continues the text and drops the
		// reasoning; Anthropic refuses a prefill once the model thought.
		p = llm.Resume(p, llm.ResumeConfig{KeepsThinking: cfg.Active().Type == config.TypeOpenAI})
	}
	return p, nil
}

// resumeCapable reports whether a provider can continue a partial reply:
// the Anthropic Messages API always (a trailing assistant message is a
// prefill), and an OpenAI-compatible endpoint only on DeepSeek's beta
// base URL, where "prefix" is documented. Anywhere else a trailing
// assistant message is answered after rather than continued, and a
// stitched reply would say its first half twice.
func resumeCapable(p config.Provider) bool {
	switch p.Type {
	case "anthropic":
		return true
	case config.TypeOpenAI:
		u, err := url.Parse(p.BaseURL)
		return err == nil && strings.HasSuffix(u.Hostname(), "deepseek.com") && strings.HasPrefix(strings.TrimSuffix(u.Path, "/"), "/beta")
	}
	return false
}

// fingerprintDir is where the behavioural fingerprints are kept when
// fingerprint is on, "" (off) otherwise or when there is no data
// directory to keep them in.
func fingerprintDir(cfg *config.Config) string {
	if cfg == nil || !cfg.Fingerprint {
		return ""
	}
	dir, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "fingerprints")
}
