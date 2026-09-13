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
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/theme"
	"github.com/xmonader/janetcode/internal/tool"
)

// ProviderConfig is the legacy "anthropic"/"openai" section; it is folded
// into Providers on load.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Config is the resolved configuration.
type Config struct {
	// RatesTable prices models the file does not list, from LiteLLM's
	// public table (internal/rates); set by main, never read from the file.
	RatesTable interface{ PriceFor(string) *llm.Price } `json:"-"`

	// Notes are things load decided on the user's behalf and should say
	// so: a provider switched because the configured one had no key.
	Notes []string `json:"-"`

	Provider  string `json:"provider"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Effort    string `json:"effort,omitempty"`
	Thinking  *bool  `json:"thinking,omitempty"`
	// Intent asks the model for a one-line reason on every tool call. It
	// buys a label the user sees, a record that survives compaction and a
	// readable narrative of the session — and it was measured at 21% more
	// context and 25% more output, with the pass rate unchanged. That is
	// too much to spend by default on a benefit nothing has measured, so
	// it is off; turn it on if the narrative is worth it to you.
	Intent *bool `json:"intent,omitempty"`
	// LeanTools defers the search tools (list_dir, glob, grep, repo_map)
	// behind tool_search, leaving read/write/edit/bash in the prefix.
	// On fan-out work this is 23% less context for an unchanged pass rate,
	// replicated on two models over 234 runs; on a large corpus it is
	// indistinguishable either way. Better or neutral everywhere measured,
	// so it is on — and the tools are deferred rather than removed, so the
	// worst case is one tool_search call to get them back.
	LeanTools_ *bool `json:"lean_tools,omitempty"`
	// ShowThinking renders the model's reasoning. Turning it off asks the
	// provider to omit the text while still returning the signature that
	// keeps multi-turn reasoning valid, which lowers time to first visible
	// token. Default true, because the terminal does show it.
	ShowThinking  *bool `json:"show_thinking,omitempty"`
	AutoApprove   bool  `json:"auto_approve"`
	MaxIterations int   `json:"max_iterations"`
	// CompactAt is the estimated token count that triggers compaction;
	// 0 means the default, negative disables.
	CompactAt int `json:"compact_at,omitempty"`
	// Theme selects the palette: auto (detect the terminal background),
	// dark, light, or the name of a builtin or user theme. See the themes
	// package for the builtins.
	Theme string `json:"theme,omitempty"`
	// Themes are user-defined palettes by name, each a role→colour map with
	// an optional base to inherit from. They override or add to the
	// builtins and are validated on load (a bad colour or an unknown role
	// is ErrBadConfig). ThemeSet is the resolved, validated collection of
	// builtins plus these plus any <cfgDir>/themes/<name>.json files; it is
	// built by load, never read from the file.
	Themes   map[string]json.RawMessage `json:"themes,omitempty"`
	ThemeSet *theme.Set                 `json:"-"`
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
	// Routes send a model name, or a prefix of one, to the provider that
	// serves it, so a model can be asked for by name alone. Without an
	// entry the name's family decides (glm-* is Zhipu), and failing that
	// the active provider is used.
	Routes map[string]string `json:"routes,omitempty"`
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
	// EmbeddingModel, when set, is asked of the provider's /embeddings
	// endpoint so memory recall ranks by meaning, not just words.
	EmbeddingModel string `json:"embedding_model,omitempty"`
	// LSP configures language servers for diagnostics after edits.
	LSP lsp.Config `json:"lsp,omitempty"`
	// Search selects the web_search backend: DuckDuckGo by default, or
	// Brave Search with a key.
	Search tool.SearchConfig `json:"search,omitempty"`
	// FallbackModels are tried, in order, when a call fails in a way
	// another model could survive: an exhausted quota, or rate limits
	// and server errors that outlived the provider's own retries. Each
	// is routed like any other model name, so a fallback may live on a
	// different provider. At most three are used.
	FallbackModels []string `json:"fallback_models,omitempty"`
	// StreamIdleTimeout is how many seconds a streamed model call may
	// produce nothing before it is given up on; every increment resets
	// it. StreamMaxDuration is the longest one call may take however
	// steadily it streams. Zero means the default (240 and 900);
	// negative disables the bound.
	StreamIdleTimeout int `json:"stream_idle_timeout,omitempty"`
	StreamMaxDuration int `json:"stream_max_duration,omitempty"`
	// Memory enables automatic extraction of project memory after turns
	// that changed something; nil means on. /remember always works.
	Memory *bool `json:"memory,omitempty"`
	// Hints enables the one-time notes about features a session could
	// have used; nil means on. Each fires at most once per project, so
	// turning them off is for somebody who knows the harness and wants
	// the transcript quiet. /hints on|off does it for one session.
	Hints *bool `json:"hints,omitempty"`
	// ProofNotes attaches machine-readable evidence to a commit this
	// session makes (see internal/proof): which session, turn and stated
	// intent wrote each of its files, and what actually demonstrated the
	// work. Nil means on; it costs one git-notes write per commit and
	// nothing when a turn made no commit, so unlike Intent there is no
	// per-request budget to weigh it against.
	ProofNotes *bool `json:"proof_notes,omitempty"`
}

// StreamLimits are the watchdog bounds for one streamed model call.
func (c *Config) StreamLimits() llm.StreamLimits {
	return llm.StreamLimits{
		Idle: time.Duration(c.StreamIdleTimeout) * time.Second,
		Max:  time.Duration(c.StreamMaxDuration) * time.Second,
	}
}

// AutoMemory reports whether memory is extracted automatically.
func (c *Config) AutoMemory() bool { return c.Memory == nil || *c.Memory }

// ShowHints reports whether one-time feature hints are enabled.
func (c *Config) ShowHints() bool { return c.Hints == nil || *c.Hints }

// ProofNotesEnabled reports whether commits made this session get proof
// notes attached automatically.
func (c *Config) ProofNotesEnabled() bool { return c.ProofNotes == nil || *c.ProofNotes }

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
		if c.RatesTable != nil {
			return c.RatesTable.PriceFor(model)
		}
		return nil
	}
	p := c.Prices[best]
	return &p
}

// ErrNoCredentials is returned (with a usable Config) when the selected
// provider has no API key, so an interactive caller can ask for one.
var ErrNoCredentials = errors.New("no API key")

// ErrBadConfig marks a configuration the user has to fix: malformed JSON,
// an unknown provider, a price table with nonsense in it, an invalid
// effort or theme. It is distinct from ErrNoCredentials, which is a
// working configuration missing only a key, and it is what the exit-code
// classifier matches on so that "exit 52" is a property of the error and
// not of the wording of its message.
var ErrBadConfig = errors.New("bad configuration")

// badf builds an ErrBadConfig error with a message of its own.
func badf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadConfig, fmt.Sprintf(format, args...))
}

// Overrides are command-line values; empty means "not given".
type Overrides struct {
	Provider    string
	Model       string
	Effort      string
	Theme       string
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
    "zhipu": {"type": "openai", "base_url": "https://open.bigmodel.cn/api/paas/v4", "api_key": "", "models": ["glm-5"]},
    "ollama": {"type": "openai", "base_url": "http://localhost:11434/v1"}
  },
  "routes": {
    "glm-5": "zhipu"
  },
  "prices": {
    "example-model": {"input": 3.0, "output": 15.0, "cache_read": 0.3, "cache_write": 3.75}
  },
  "mcp_servers": {}
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
			return nil, badf("config %s: %v", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, badf("config %s: %v", path, err)
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
	if ov.Theme != "" {
		cfg.Theme = ov.Theme
	}
	if ov.AutoApprove {
		cfg.AutoApprove = true
	}

	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = inferProvider(cfg)
	} else if ov.Provider == "" && getenv("JANETCODE_PROVIDER") == "" {
		// The file names a provider that has no key while another does.
		// The --init template names anthropic; a user who then exports
		// an OpenAI-compatible key must not be told there is no key.
		// A provider chosen on the command line or in the environment
		// is honoured as given, and a local server needs no key.
		if p, ok := cfg.Providers[cfg.Provider]; ok && len(p.Keys()) == 0 && p.NeedsKey() {
			if alt := inferProvider(cfg); alt != cfg.Provider && len(cfg.Providers[alt].Keys()) > 0 {
				cfg.Notes = append(cfg.Notes, fmt.Sprintf("provider %q has no API key; using %q, which has one (--provider %s insists on it)", cfg.Provider, alt, cfg.Provider))
				cfg.Provider = alt
			}
		}
	}
	active, ok := cfg.Providers[cfg.Provider]
	if !ok {
		pre, isPreset := FindPreset(cfg.Provider)
		if !isPreset {
			return nil, badf("unknown provider %q (configured: %s; presets: %s)", cfg.Provider, orNone(cfg.ProviderNames()), presetNames())
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
			return nil, badf("provider %q needs a model: pass --model, set JANETCODE_MODEL, or set model in the config file (for example JANETCODE_MODEL=deepseek/deepseek-v4-flash)", cfg.Provider)
		}
		if len(active.Keys()) == 0 && active.NeedsKey() {
			if cfg.Provider == "openai" {
				return cfg, fmt.Errorf("%w: set OPENAI_API_KEY or providers.openai.api_key in %s", ErrNoCredentials, path)
			}
			return cfg, fmt.Errorf("%w for provider %q: set providers.%s.api_key in %s", ErrNoCredentials, cfg.Provider, cfg.Provider, path)
		}
	default:
		return nil, badf("provider %q has type %q (want anthropic or openai)", cfg.Provider, active.Type)
	}
	switch cfg.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return nil, badf("invalid effort %q (want low, medium, high, xhigh or max)", cfg.Effort)
	}
	// Build the theme set from the builtins, the config "themes" object and
	// any theme files in <cfgDir>/themes. A bad user theme is a config
	// error, not a silent fallback, and it names the theme and the field.
	userThemes, err := theme.ParseMap(cfg.Themes)
	if err != nil {
		return nil, badf("%s: %v", path, err)
	}
	userThemes, err = theme.LoadDir(filepath.Join(filepath.Dir(path), "themes"), userThemes)
	if err != nil {
		return nil, badf("%s: themes: %v", path, err)
	}
	set, err := theme.Build(userThemes)
	if err != nil {
		return nil, badf("%s: %v", path, err)
	}
	cfg.ThemeSet = set
	switch cfg.Theme {
	case "", "auto", "dark", "light":
	default:
		if !set.Has(cfg.Theme) {
			return nil, badf("unknown theme %q (want auto, dark, light or one of: %s)", cfg.Theme, strings.Join(set.Names(), ", "))
		}
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = DefaultMaxIter
	}
	if err := cfg.Hooks.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if _, err := cfg.Permissions.Rules(); err != nil {
		return nil, badf("%s: permissions: %v", path, err)
	}
	if err := cfg.LSP.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := cfg.Search.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := validatePrices(cfg.Prices); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	return cfg, nil
}

// validatePrices rejects a price table that cannot mean what it says. A
// negative rate would make a run appear to earn money, and a price with
// every field zero is almost always a half-finished entry that silently
// reports every turn as free; both are better caught at load than
// discovered in a cost report.
func validatePrices(prices map[string]llm.Price) error {
	for name, p := range prices {
		for _, f := range []struct {
			field string
			v     float64
		}{{"input", p.Input}, {"output", p.Output}, {"cache_read", p.CacheRead}, {"cache_write", p.CacheWrite}} {
			if f.v < 0 {
				return fmt.Errorf("prices[%q].%s is %g: a price per million tokens cannot be negative", name, f.field, f.v)
			}
		}
		if p.Input == 0 && p.Output == 0 && p.CacheRead == 0 && p.CacheWrite == 0 {
			return fmt.Errorf("prices[%q] is all zeroes: remove the entry, or give it the rates per million tokens", name)
		}
	}
	return nil
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

// IntentEnabled reports whether tool calls carry a stated reason.
func (c *Config) IntentEnabled() bool {
	if c.Intent != nil {
		return *c.Intent
	}
	return false
}

// LeanTools reports whether the search tools are deferred.
func (c *Config) LeanTools() bool {
	if c.LeanTools_ != nil {
		return *c.LeanTools_
	}
	return true
}

// ThinkingShown reports whether the reasoning text should be requested at
// all. When false the provider is asked to omit it: nothing is rendered,
// nothing is billed for it, and the first visible token arrives sooner.
func (c *Config) ThinkingShown() bool {
	if c.ShowThinking != nil {
		return *c.ShowThinking
	}
	return true
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
