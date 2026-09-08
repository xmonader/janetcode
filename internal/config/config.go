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

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/perm"
)

// ProviderConfig is the legacy "anthropic"/"openai" section; it is folded
// into Providers on load.
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
	Theme string `json:"theme,omitempty"`
	// Providers are the backends by name; Provider selects one. Presets
	// (see Presets) can be referred to by name without a base_url.
	Providers map[string]Provider `json:"providers,omitempty"`
	Anthropic ProviderConfig      `json:"anthropic,omitempty"`
	OpenAI    ProviderConfig      `json:"openai,omitempty"`
	// Prices per million tokens by model name, used to estimate spend when
	// the provider does not report it. A key may be a prefix ("claude-opus").
	Prices map[string]llm.Price `json:"prices,omitempty"`
	// Models holds what /connect learned about models: context window,
	// output limit and prices. A key may be a prefix like Prices.
	Models map[string]ModelMeta `json:"models,omitempty"`
	// MCPServers are started for every project. Project-specific ones go
	// in <project>/.mcp.json and need a one-time approval.
	MCPServers map[string]mcp.ServerConfig `json:"mcp_servers,omitempty"`
	// Hooks run for every project. Project-specific ones go in
	// <project>/.janetcode/hooks.json and need a one-time approval.
	Hooks hook.Config `json:"hooks,omitempty"`
	// Permissions are persistent allow/deny rules, e.g. "bash(go test *)".
	// Project rules go in <project>/.janetcode/permissions.json; its allow
	// rules need a one-time approval, its deny rules always apply.
	Permissions perm.File `json:"permissions,omitempty"`
	// LSP configures language servers for diagnostics after edits.
	LSP lsp.Config `json:"lsp,omitempty"`
	// Memory enables automatic extraction of project memory after turns
	// that changed something; nil means on. /remember always works.
	Memory *bool `json:"memory,omitempty"`
}

// AutoMemory reports whether memory is extracted automatically.
func (c *Config) AutoMemory() bool { return c.Memory == nil || *c.Memory }

// ModelMeta is what a provider's model list said about one model.
type ModelMeta struct {
	ContextWindow int        `json:"context_window,omitempty"`
	MaxOutput     int        `json:"max_output,omitempty"`
	Price         *llm.Price `json:"price,omitempty"`
}

// MetaFor returns the stored metadata for a model, by exact name or the
// longest prefix, with the context window guessed for known families.
func (c *Config) MetaFor(model string) ModelMeta {
	var meta ModelMeta
	if m, ok := c.Models[model]; ok {
		meta = m
	} else {
		best := ""
		for k := range c.Models {
			if strings.HasPrefix(model, k) && len(k) > len(best) {
				best = k
			}
		}
		if best != "" {
			meta = c.Models[best]
		}
	}
	if meta.ContextWindow == 0 {
		meta.ContextWindow = llm.KnownWindow(model)
	}
	return meta
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
		if m := c.MetaFor(model); m.Price != nil {
			return m.Price
		}
		return nil
	}
	p := c.Prices[best]
	return &p
}

// ErrNoCredentials is returned (with a usable Config) when the selected
// provider has no API key, so an interactive caller can ask for one.
var ErrNoCredentials = errors.New("no API key")

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
  "compact_at": 80000,
  "theme": "auto",
  "providers": {
    "anthropic": {"type": "anthropic", "api_key": ""},
    "openrouter": {"type": "openai", "base_url": "https://openrouter.ai/api/v1", "api_key": "", "api_keys": [], "model": "deepseek/deepseek-v4-flash"},
    "ollama": {"type": "openai", "base_url": "http://localhost:11434/v1"}
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
// overrides, and validates the result. When the only problem is a missing
// key it returns the Config together with an error wrapping
// ErrNoCredentials.
func Load(ov Overrides) (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return load(p, os.Getenv, ov)
}

// inferProvider picks a backend when none was configured: Anthropic when
// it has a key, else the first configured provider with one, else Anthropic.
func inferProvider(cfg *Config) string {
	if len(cfg.Providers[DefaultProvider].Keys()) > 0 {
		return DefaultProvider
	}
	for _, n := range cfg.ProviderNames() {
		if len(cfg.Providers[n].Keys()) > 0 {
			return n
		}
	}
	return DefaultProvider
}

// mergeProviders folds the legacy sections and environment into Providers.
func mergeProviders(cfg *Config, getenv func(string) string) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	fold := func(name, typ string, legacy ProviderConfig, keyEnv, urlEnv string) {
		e, ok := cfg.Providers[name]
		if !ok {
			if legacy.APIKey == "" && legacy.BaseURL == "" && getenv(keyEnv) == "" && getenv(urlEnv) == "" {
				return
			}
			e = Provider{Type: typ}
		}
		if e.Type == "" {
			e.Type = typ
		}
		if e.APIKey == "" && len(e.APIKeys) == 0 {
			e.APIKey = legacy.APIKey
		}
		if e.BaseURL == "" {
			e.BaseURL = legacy.BaseURL
		}
		if k := getenv(keyEnv); k != "" {
			e.SetKeys(strings.Split(k, ","))
			e.envKeys = true
		}
		if u := getenv(urlEnv); u != "" {
			e.BaseURL = u
		}
		cfg.Providers[name] = e
	}
	fold("anthropic", "anthropic", cfg.Anthropic, "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL")
	fold("openai", "openai", cfg.OpenAI, "OPENAI_API_KEY", "OPENAI_BASE_URL")
	for n, e := range cfg.Providers {
		if pre, ok := FindPreset(n); ok {
			if e.Type == "" {
				e.Type = pre.Type
			}
			if e.BaseURL == "" {
				e.BaseURL = pre.BaseURL
			}
		}
		e.Type = strings.ToLower(strings.TrimSpace(e.Type))
		cfg.Providers[n] = e
	}
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
	mergeProviders(cfg, getenv)
	fileProvider, fileModel := cfg.Provider, cfg.Model
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
	active, ok := cfg.Providers[cfg.Provider]
	if !ok {
		pre, isPreset := FindPreset(cfg.Provider)
		if !isPreset {
			return nil, fmt.Errorf("unknown provider %q (configured: %s; presets: %s)", cfg.Provider, orNone(cfg.ProviderNames()), presetNames())
		}
		active = Provider{Type: pre.Type, BaseURL: pre.BaseURL}
		cfg.Providers[cfg.Provider] = active
	}
	// A provider chosen on the command line or in the environment brings
	// its own last-used model; the file's top-level model belongs to the
	// file's provider.
	if cfg.Model == "" || (cfg.Provider != fileProvider && cfg.Model == fileModel) {
		if active.Model != "" {
			cfg.Model = active.Model
		}
	}
	switch active.Type {
	case "anthropic":
		if cfg.Model == "" {
			cfg.Model = DefaultModel
		}
		if len(active.Keys()) == 0 {
			if cfg.Provider == DefaultProvider {
				return cfg, fmt.Errorf("%w: set ANTHROPIC_API_KEY, or OPENAI_API_KEY with JANETCODE_MODEL for an OpenAI-compatible backend, or run janetcode --init and edit %s", ErrNoCredentials, path)
			}
			return cfg, fmt.Errorf("%w for provider %q: set providers.%s.api_key in %s", ErrNoCredentials, cfg.Provider, cfg.Provider, path)
		}
	case "openai":
		if cfg.Model == "" {
			return nil, fmt.Errorf("provider %q needs a model: pass --model, set JANETCODE_MODEL, or set model in the config file (for example JANETCODE_MODEL=deepseek/deepseek-v4-flash)", cfg.Provider)
		}
		if len(active.Keys()) == 0 && active.NeedsKey() {
			if cfg.Provider == "openai" {
				return cfg, fmt.Errorf("%w: set OPENAI_API_KEY or providers.openai.api_key in %s", ErrNoCredentials, path)
			}
			return cfg, fmt.Errorf("%w for provider %q: set providers.%s.api_key in %s", ErrNoCredentials, cfg.Provider, cfg.Provider, path)
		}
	default:
		return nil, fmt.Errorf("provider %q has type %q (want anthropic or openai)", cfg.Provider, active.Type)
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
	if err := cfg.Hooks.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := cfg.Permissions.Rules(); err != nil {
		return nil, fmt.Errorf("%s: permissions: %w", path, err)
	}
	if err := cfg.LSP.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func presetNames() string {
	names := make([]string, len(Presets))
	for i, p := range Presets {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
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
