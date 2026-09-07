// Package config loads janetcode settings from the config file, environment
// and command-line overrides, in that order of increasing precedence.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderConfig is the credentials for one backend.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Config is the resolved configuration.
type Config struct {
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	MaxTokens     int            `json:"max_tokens"`
	Effort        string         `json:"effort,omitempty"`
	Thinking      *bool          `json:"thinking,omitempty"`
	AutoApprove   bool           `json:"auto_approve"`
	MaxIterations int            `json:"max_iterations"`
	// CompactAt is the estimated token count that triggers compaction;
	// 0 means the default, negative disables.
	CompactAt int `json:"compact_at,omitempty"`
	// Theme is dark, light or auto.
	Theme string `json:"theme,omitempty"`
	Anthropic     ProviderConfig `json:"anthropic"`
	OpenAI        ProviderConfig `json:"openai"`
}

// Overrides are command-line values; empty means "not given".
type Overrides struct {
	Provider    string
	Model       string
	Effort      string
	AutoApprove bool
}

const (
	DefaultProvider  = "anthropic"
	DefaultModel     = "claude-opus-5"
	DefaultMaxTokens = 16000
	DefaultMaxIter   = 50
)

// Path returns the config file location, honouring XDG_CONFIG_HOME.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "janetcode", "config.json"), nil
}

// Load reads the config file (if any), applies environment variables, then
// overrides, and validates the result.
func Load(ov Overrides) (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return load(p, os.Getenv, ov)
}

func load(path string, getenv func(string) string, ov Overrides) (*Config, error) {
	cfg := &Config{
		Provider:      DefaultProvider,
		Model:         "",
		MaxTokens:     DefaultMaxTokens,
		MaxIterations: DefaultMaxIter,
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	setIf := func(dst *string, key string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	setIf(&cfg.Anthropic.APIKey, "ANTHROPIC_API_KEY")
	setIf(&cfg.Anthropic.BaseURL, "ANTHROPIC_BASE_URL")
	setIf(&cfg.OpenAI.APIKey, "OPENAI_API_KEY")
	setIf(&cfg.OpenAI.BaseURL, "OPENAI_BASE_URL")
	setIf(&cfg.Provider, "JANETCODE_PROVIDER")
	setIf(&cfg.Model, "JANETCODE_MODEL")
	setIf(&cfg.Effort, "JANETCODE_EFFORT")
	setIf(&cfg.Theme, "JANETCODE_THEME")

	if ov.Provider != "" {
		cfg.Provider = ov.Provider
	}
	if ov.Model != "" {
		cfg.Model = ov.Model
	}
	if ov.Effort != "" {
		cfg.Effort = ov.Effort
	}
	if ov.AutoApprove {
		cfg.AutoApprove = true
	}

	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch cfg.Provider {
	case "anthropic":
		if cfg.Model == "" {
			cfg.Model = DefaultModel
		}
		if cfg.Anthropic.APIKey == "" {
			return nil, fmt.Errorf("no Anthropic API key: set ANTHROPIC_API_KEY or anthropic.api_key in %s", path)
		}
	case "openai":
		if cfg.Model == "" {
			return nil, errors.New("openai provider needs a model: pass --model, set JANETCODE_MODEL, or set model in the config file")
		}
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("no OpenAI-compatible API key: set OPENAI_API_KEY or openai.api_key in %s", path)
		}
	case "":
		return nil, errors.New("provider is empty")
	default:
		return nil, fmt.Errorf("unknown provider %q (want anthropic or openai)", cfg.Provider)
	}
	switch cfg.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return nil, fmt.Errorf("invalid effort %q (want low, medium, high, xhigh or max)", cfg.Effort)
	}
	switch cfg.Theme {
	case "", "auto", "dark", "light":
	default:
		return nil, fmt.Errorf("invalid theme %q (want auto, dark or light)", cfg.Theme)
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = DefaultMaxIter
	}
	return cfg, nil
}

// ThinkingEnabled reports whether adaptive thinking should be requested.
// Default on; off for Haiku 4.5 and older models that reject it.
func (c *Config) ThinkingEnabled() bool {
	if c.Thinking != nil {
		return *c.Thinking
	}
	m := strings.ToLower(c.Model)
	return !strings.Contains(m, "haiku") && !strings.Contains(m, "claude-3")
}
