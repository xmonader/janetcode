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

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
)

// ProviderConfig is the credentials for one backend.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Config is the resolved configuration.
type Config struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	MaxTokens     int    `json:"max_tokens"`
	Effort        string `json:"effort,omitempty"`
	Thinking      *bool  `json:"thinking,omitempty"`
	AutoApprove   bool   `json:"auto_approve"`
	MaxIterations int    `json:"max_iterations"`
	// CompactAt is the estimated token count that triggers compaction;
	// 0 means the default, negative disables.
	CompactAt int `json:"compact_at,omitempty"`
	// Theme is dark, light or auto.
	Theme     string         `json:"theme,omitempty"`
	Anthropic ProviderConfig `json:"anthropic"`
	OpenAI    ProviderConfig `json:"openai"`
	// Prices per million tokens by model name, used to estimate spend when
	// the provider does not report it. A key may be a prefix ("claude-opus").
	Prices map[string]llm.Price `json:"prices,omitempty"`
	// MCPServers are started for every project. Project-specific ones go
	// in <project>/.mcp.json and need a one-time approval.
	MCPServers map[string]mcp.ServerConfig `json:"mcp_servers,omitempty"`
}

// PriceFor returns the configured price for model: an exact key first, then
// the longest key that prefixes the model name.
func (c *Config) PriceFor(model string) *llm.Price {
	if p, ok := c.Prices[model]; ok {
		return &p
	}
	best := ""
	for k := range c.Prices {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return nil
	}
	p := c.Prices[best]
	return &p
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

// Example is the config file written by WriteExample.
const Example = `{
  "provider": "anthropic",
  "model": "claude-opus-5",
  "max_tokens": 16000,
  "effort": "high",
  "auto_approve": false,
  "max_iterations": 50,
  "compact_at": 150000,
  "theme": "auto",
  "anthropic": {
    "api_key": "",
    "base_url": ""
  },
  "openai": {
    "api_key": "",
    "base_url": "https://api.openai.com/v1"
  },
  "prices": {
    "example-model": {"input": 3.0, "output": 15.0, "cache_read": 0.3, "cache_write": 3.75}
  },
  "mcp_servers": {
    "example": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."], "env": {}}
  }
}
`

// WriteExample writes Example to the config path unless a file already
// exists there. It returns the path.
func WriteExample() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return p, fmt.Errorf("%s already exists", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(Example), 0o600); err != nil {
		return "", err
	}
	return p, nil
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

// inferProvider picks a backend when none was configured: the one whose key
// is present, Anthropic when both or neither are.
func inferProvider(cfg *Config) string {
	if cfg.Anthropic.APIKey == "" && cfg.OpenAI.APIKey != "" {
		return "openai"
	}
	return DefaultProvider
}

func load(path string, getenv func(string) string, ov Overrides) (*Config, error) {
	cfg := &Config{
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
	if cfg.Provider == "" {
		cfg.Provider = inferProvider(cfg)
	}
	switch cfg.Provider {
	case "anthropic":
		if cfg.Model == "" {
			cfg.Model = DefaultModel
		}
		if cfg.Anthropic.APIKey == "" {
			return nil, fmt.Errorf("no API key: set ANTHROPIC_API_KEY, or OPENAI_API_KEY with JANETCODE_MODEL for an OpenAI-compatible backend, or run janetcode --init and edit %s", path)
		}
	case "openai":
		if cfg.Model == "" {
			return nil, errors.New("openai provider needs a model: pass --model, set JANETCODE_MODEL, or set model in the config file (for example JANETCODE_MODEL=deepseek/deepseek-v4-flash)")
		}
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("no OpenAI-compatible API key: set OPENAI_API_KEY or openai.api_key in %s", path)
		}
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
