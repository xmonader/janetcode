// Package config loads janetcode settings from the config file, environment
// and command-line overrides, in that order of increasing precedence.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/keymap"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/netcfg"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/statusline"
	"github.com/xmonader/janetcode/internal/theme"
	"github.com/xmonader/janetcode/internal/weburl"
	"github.com/xmonader/janetcode/internal/xdg"
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

	// Managed is the administrator's managed policy (see package
	// managed), nil when there is none. load has already applied its
	// settings; /config, --set and the mode switches consult it for what
	// they may not change.
	Managed *managed.Policy `json:"-"`

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
	// CAFile is a PEM file of extra certificate authorities every
	// outbound HTTPS connection trusts beside the system's (a
	// TLS-intercepting proxy, a private gateway). JANETCODE_CA_FILE adds
	// one from the environment. See netcfg.
	CAFile string `json:"ca_file,omitempty"`
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
	// Keybindings move the terminal interface's prompt keys: action name
	// to a key, a list of keys, or "" to unbind it ("history_search":
	// "ctrl+r", "effort": ["alt+e", "f2"]). An action named here loses its
	// default keys. Unknown actions, keys bubbletea cannot report, plain
	// characters and a key left on two actions are ErrBadConfig. KeyMap
	// is the resolved table, built by load.
	Keybindings keymap.Overrides `json:"keybindings,omitempty"`
	KeyMap      *keymap.Map      `json:"-"`
	// VimMode starts the composer in vim mode (normal/insert); /vim
	// toggles it for the session.
	VimMode bool `json:"vim_mode,omitempty"`
	// StatusLine is a command whose output the terminal interface shows
	// under the input; it gets the session as JSON on stdin.
	StatusLine *statusline.Config `json:"status_line,omitempty"`
	// StatusLineCompat is the same object under the name Claude Code and
	// Qwen Code settings give it, so one can be pasted in as it is.
	StatusLineCompat *statusline.Config `json:"statusLine,omitempty"`
	// Permissions are persistent allow/deny rules, e.g. "bash(go test *)".
	// Project rules go in <project>/.janetcode/permissions.json; its allow
	// rules need a one-time approval, its deny rules always apply.
	Permissions perm.File `json:"permissions,omitempty"`
	// ProtectedPaths are extra path patterns a write always asks about,
	// whatever the mode or the rules say: they are appended to
	// perm.DefaultProtectedPaths (shell startup files, .git/config and
	// hooks, .npmrc, .ssh, cloud credentials, janetcode's permission
	// record) and never replace them. A pattern is matched the way a
	// permission rule is — * spans anything, including separators — and a
	// leading ~ is the home directory: "~/work/deploy/**".
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	// EmbeddingModel, when set, is asked of the provider's /embeddings
	// endpoint so memory recall ranks by meaning, not just words.
	EmbeddingModel string `json:"embedding_model,omitempty"`
	// LSP configures language servers for diagnostics after edits.
	LSP lsp.Config `json:"lsp,omitempty"`
	// Search selects the web_search backend: DuckDuckGo by default, or
	// Brave Search with a key.
	Search weburl.SearchConfig `json:"search,omitempty"`
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
	// StaleCheckNudge sends a turn back once when the turn's checks
	// passed, it edited again afterwards, and it then said it was done:
	// everything that passed, passed against a tree the turn has since
	// changed. Off by default, and bounded to one nudge a turn.
	StaleCheckNudge bool `json:"stale_check_nudge,omitempty"`
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
	// Sandbox names the sandbox profile a session in this project starts
	// under: "workspace", "read-only" or "strict" (see internal/shell's
	// profile catalogue). Empty, or "off", is the default: no sandbox,
	// bash runs on the workspace host. A project that sets it has every
	// session start sandboxed, and /sandbox still changes it for one
	// session.
	Sandbox string `json:"sandbox,omitempty"`
	// RouteCheap is an experimental, off-by-default model name: when set,
	// a turn's mechanical steps (reads, listings, greps, a check that
	// already passed) are sent to it instead of Model, which stays on
	// planning, edits and anything that goes wrong (see
	// internal/agent/steproute.go for the exact policy). It is resolved
	// through the same routing RouteFor gives a fallback; a name nothing
	// can route turns the feature off for the session with one notice.
	// Switching models mid-turn breaks the provider's prompt cache (see
	// internal/agent/cachestat.go), so this is a bet that the tokens
	// saved by a cheaper model outweigh a cache miss on the switch — it
	// is off by default until an eval run says that bet pays off; see
	// docs/manual/evals.md, "Measuring step routing".
	RouteCheap string `json:"route_cheap,omitempty"`
	// Followup enables a cheap next-prompt suggestion as ghost text
	// after a turn; tab accepts it. Nil means off.
	Followup *bool `json:"followup,omitempty"`
	// ComputerUse starts Cua Driver as an MCP server when cua-driver (or
	// open-computer-use) is on PATH. Nil means off; /cua on sets it.
	ComputerUse *bool `json:"computer_use,omitempty"`
	// AdvisorMode is how the harness forces advisor consults, as opposed
	// to offering the advisor tool and hoping the model presses it:
	// "off" (the default) or "always". A consult returns guidance text
	// injected into the executor's context; the advisor never acts.
	AdvisorMode string `json:"advisor_mode,omitempty"`
	// AdvisorMaxConsults caps forced consults per turn. 0 is two.
	AdvisorMaxConsults int `json:"advisor_max_consults,omitempty"`
	// StrongModel is the strong slot of the cheap/default/strong trio:
	// route_cheap names the cheap one, model the default, this one the
	// model a failing turn escalates to. Empty means there is no tier
	// above the session's own and the cascade cannot fire.
	StrongModel string `json:"strong_model,omitempty"`
	// EscalateAfter is how many verification rounds the session's own
	// model may fail before the rest of the turn runs on StrongModel.
	// Zero is off: a cascade changes what a run costs, so it is never on
	// without being asked for. The trigger is the real check failing,
	// never a judgement about how hard the request sounds.
	EscalateAfter int `json:"escalate_after,omitempty"`
	// VisionModel is the slot for a model that reads images. Set, it
	// declares the session's own model unable to: every image a request
	// would carry to another model is described by this one, and the
	// description is sent instead (see agent/vision.go).
	VisionModel string `json:"vision_model,omitempty"`
	// AdvisorModel is the model the deferred advisor tool consults.
	// Empty uses the session model. The tool is never on the always-on
	// schema list.
	AdvisorModel string `json:"advisor_model,omitempty"`
	// Decisions configures the optional TypeSafe Jev decision client
	// (internal/jev): typed answers to narrow questions around the agent
	// loop. Nil means the feature is invisible. When the block is
	// present each consumer is still off until its own key says so.
	Decisions *DecisionsConfig `json:"decisions,omitempty"`
}

// DecisionsConfig is the "decisions" config block. Everything about it
// is advisory: no answer it produces can permit, refuse or edit — the
// deterministic guards never read it.
type DecisionsConfig struct {
	// Enabled is the master switch /jev on|off writes. Nil is on, so a
	// block written before the switch existed behaves exactly as it did:
	// present means available, and each consumer is still off until its
	// own key says so. False turns the whole feature off without
	// forgetting the model, the endpoint or which consumers were on.
	// JANETCODE_OFF=decisions overrides it either way.
	Enabled *bool `json:"enabled,omitempty"`
	// BaseURL is the API endpoint; empty is TypeSafe's. A gateway or a
	// test server goes here. http is accepted only for loopback.
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the key; empty is
	// OPENROUTER_API_KEY (the default endpoint is OpenRouter's; a
	// native TypeSafe base_url usually wants TYPESAFE_API_KEY). The key
	// itself never goes in the file.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Model is the pinned, versioned Jev id (jev-1.13.0). Aliases are
	// refused: a threshold tuned against one version must not move
	// underfoot when jev-latest does.
	Model string `json:"model"`
	// TimeoutMs bounds one decision call; 0 is the client's default and
	// anything past the client's ceiling is refused here rather than
	// discovered as slow turns.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// Triage enables verify-failure triage (Phase 1 of
	// docs/notes/jev-integration-spec.md). Trajectory and Intake are
	// reserved for the later phases and currently wire nothing.
	Triage bool `json:"triage,omitempty"`
	// Receipts enables the evidence-preserving receipt consumer
	// (SoL-Pi's EPR, Phase 0 prototype verified in docs/notes/jev.md):
	// oversized tool outputs get one cheap side call on the session's
	// own provider; the receipt stands only when every quoted line is
	// byte-exact in the archived output, else the head+tail preview
	// stands untouched. Side model: advisor_model, else the session's.
	Trajectory bool `json:"trajectory,omitempty"`
	Intake     bool `json:"intake,omitempty"`
	// IntakeRoute lets the intake classification act: a first prompt it
	// is confident is small and read-only starts on route_cheap instead
	// of the session's model. It needs Intake (there is otherwise no
	// classification to read) and route_cheap (there is otherwise
	// nowhere to route), and does nothing without both.
	IntakeRoute bool `json:"intake_route,omitempty"`
	// SessionOutcome enables the session-end outcome classification
	// (item 9): one call after the last turn, logged, counted, and
	// offered to sleep consolidation. Nothing is denied or changed
	// because of the answer.
	SessionOutcome bool `json:"session_outcome,omitempty"`
	Receipts       bool `json:"receipts,omitempty"`
}

// Validate rejects a decisions block that cannot work, at load, the same
// way a bad theme or price table is rejected.
func (d *DecisionsConfig) Validate() error {
	if d == nil {
		return nil
	}
	if !jev.PinnedModel(d.Model) {
		return fmt.Errorf("decisions.model %q is not a pinned version (want jev-X.Y.Z; aliases like jev-latest are refused on purpose)", d.Model)
	}
	if d.TimeoutMs < 0 || d.TimeoutMs > int(jev.MaxTimeout/time.Millisecond) {
		return fmt.Errorf("decisions.timeout_ms is %d (want 0 for the default, or at most %d)", d.TimeoutMs, jev.MaxTimeout/time.Millisecond)
	}
	if u := d.BaseURL; u != "" {
		if strings.HasPrefix(u, "http://") {
			// The host as the client will dial it: cutting at the first
			// ":" or "/" took "localhost" from http://localhost:80@host/
			// (a user name) and "[" from http://[::1]:9000.
			h := ""
			if pu, err := url.Parse(u); err == nil {
				h = pu.Hostname()
			}
			if h != "localhost" && h != "127.0.0.1" && h != "::1" {
				return fmt.Errorf("decisions.base_url %q is plain http off loopback; states carry command output, so the connection must be https", u)
			}
		} else if !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("decisions.base_url %q wants an https URL", u)
		}
	}
	return nil
}

// On reports whether the decisions feature is available at all: a block
// that exists and has not been switched off. It says nothing about the
// consumers, each of which has its own key.
func (d *DecisionsConfig) On() bool {
	return d != nil && (d.Enabled == nil || *d.Enabled)
}

// KeyEnv is the environment variable the key is read from. The default
// follows the default endpoint: OpenRouter.
func (d *DecisionsConfig) KeyEnv() string {
	if d.APIKeyEnv != "" {
		return d.APIKeyEnv
	}
	return "OPENROUTER_API_KEY"
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

// Keys are the config file's top-level keys, read off the struct's json
// tags so the list cannot drift from what load accepts. The feature
// catalogue documents them and the doctor uses them to recognise a
// misspelling.
func Keys() []string {
	t := reflect.TypeOf(Config{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// badf builds an ErrBadConfig error with a message of its own.
// ErrBadOverride marks a command-line value config.Load cannot use: a
// usage error (exit 42), where ErrBadConfig is the file's fault (52).
var ErrBadOverride = errors.New("bad command-line value")

type badOverride struct{ msg string }

func (e badOverride) Error() string { return e.msg }
func (e badOverride) Unwrap() error { return ErrBadOverride }

// badValue is badf for a value that may have come from a flag: named
// after the flag and marked ErrBadOverride when it did.
func badValue(fromFlag bool, flagName, format string, args ...any) error {
	if fromFlag {
		return badOverride{"--" + flagName + ": " + fmt.Sprintf(format, args...)}
	}
	return badf(format, args...)
}

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
	dir, err := xdg.ConfigDir(os.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
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
	cfg, err := load(p, os.Getenv, ov)
	// The extra roots go in before any client is built from this
	// configuration: a provider, MCP or fetch_url made first would
	// refuse the proxy's certificate for the rest of the process.
	if cfg != nil && cfg.CAFile != "" {
		if _, caErr := netcfg.AddCAFile(cfg.CAFile); caErr != nil {
			return nil, badf("config %s: ca_file: %v", p, caErr)
		}
	}
	return cfg, err
}

// inferProvider picks a backend when none was configured: Anthropic when
// it has a key, else the first configured provider with one, else the
// first local server that needs none (OPENAI_BASE_URL pointed at Ollama
// is a whole setup), else Anthropic.
func inferProvider(cfg *Config) string {
	if len(cfg.Providers[DefaultProvider].Keys()) > 0 {
		return DefaultProvider
	}
	for _, n := range cfg.ProviderNames() {
		if len(cfg.Providers[n].Keys()) > 0 {
			return n
		}
	}
	for _, n := range cfg.ProviderNames() {
		if !cfg.Providers[n].NeedsKey() {
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
	fold := func(name, typ string, legacy ProviderConfig, keyEnv, tokenEnv, urlEnv string) {
		e, ok := cfg.Providers[name]
		if !ok {
			if legacy.APIKey == "" && legacy.BaseURL == "" && getenv(keyEnv) == "" && (tokenEnv == "" || getenv(tokenEnv) == "") && getenv(urlEnv) == "" {
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
			if typ == "anthropic" {
				e.Auth = ""
			}
		} else if t := getenv(tokenEnv); t != "" {
			// ANTHROPIC_AUTH_TOKEN, as Claude Code reads it: the same
			// key, sent as a bearer token, which is what gateways in
			// front of the Messages API ask for. An x-api-key variable
			// set beside it wins, as it does in the SDK.
			e.SetKeys(strings.Split(t, ","))
			e.envKeys = true
			e.Auth = "bearer"
		}
		if u := getenv(urlEnv); u != "" {
			e.BaseURL = u
		}
		cfg.Providers[name] = e
	}
	fold("anthropic", "anthropic", cfg.Anthropic, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL")
	fold("openai", "openai", cfg.OpenAI, "OPENAI_API_KEY", "", "OPENAI_BASE_URL")
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
		// Decode stops after the first value: a stray closing brace, or a
		// second object pasted below the first whose settings would be
		// silently dropped, is as malformed as a missing comma.
		if _, err := dec.Token(); err != io.EOF {
			if err == nil {
				err = fmt.Errorf("unexpected data after the top-level object at offset %d", dec.InputOffset())
			}
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
	for _, n := range cfg.ProviderNames() {
		switch strings.ToLower(strings.TrimSpace(cfg.Providers[n].Auth)) {
		case "", "api_key", "bearer":
		default:
			return nil, badf("config %s: providers.%s.auth: %q is not one of api_key, bearer", path, n, cfg.Providers[n].Auth)
		}
	}
	// Normalised as cfg.Provider is below, so "OpenRouter" in the file
	// is recognised as the provider the file's model belongs to.
	fileProvider, fileModel := strings.ToLower(strings.TrimSpace(cfg.Provider)), cfg.Model
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
	pol, err := managed.Load(getenv)
	if err != nil {
		return nil, badf("%v", err)
	}
	if err := applyManaged(cfg, pol, ov); err != nil {
		return nil, err
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
			return nil, badValue(ov.Provider != "", "provider", "unknown provider %q (configured: %s; presets: %s)", cfg.Provider, orNone(cfg.ProviderNames()), presetNames())
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
	// Missing credentials do not stop the load: the interactive first run
	// opens on /connect and needs a configuration that is finished --
	// themes built, limits clamped, the rest of the file validated. It
	// used to return here, so a first run silently had no user themes, no
	// clamped max_tokens, and learned about a malformed hooks or prices
	// section only on the run after the key was set.
	var credErr error
	switch active.Type {
	case "anthropic":
		if cfg.Model == "" {
			cfg.Model = DefaultModel
		}
		if len(active.Keys()) == 0 {
			if cfg.Provider == DefaultProvider {
				// The first thing to try is the one with no file to edit:
				// the TUI and --web both open on a connect form.
				credErr = fmt.Errorf("%w. Either:\n"+
					"  - run janetcode (or janetcode --web) in a terminal and /connect a provider\n"+
					"  - set ANTHROPIC_API_KEY\n"+
					"  - set OPENAI_API_KEY and JANETCODE_MODEL (plus OPENAI_BASE_URL if not OpenAI)\n"+
					"  config file: %s", ErrNoCredentials, path)
			} else {
				credErr = fmt.Errorf("%w for provider %q: run janetcode and /connect to paste one, or set providers.%s.api_key in %s", ErrNoCredentials, cfg.Provider, cfg.Provider, path)
			}
		}
	case "openai":
		if cfg.Model == "" {
			return nil, badf("provider %q needs a model: pass --model, set JANETCODE_MODEL, or set model in the config file (for example JANETCODE_MODEL=deepseek/deepseek-v4-flash)", cfg.Provider)
		}
		if len(active.Keys()) == 0 && active.NeedsKey() {
			if cfg.Provider == "openai" {
				credErr = fmt.Errorf("%w: run janetcode and /connect to paste one, or set OPENAI_API_KEY or providers.openai.api_key in %s", ErrNoCredentials, path)
			} else {
				credErr = fmt.Errorf("%w for provider %q: run janetcode and /connect to paste one, or set providers.%s.api_key in %s", ErrNoCredentials, cfg.Provider, cfg.Provider, path)
			}
		}
	default:
		return nil, badf("provider %q has type %q (want anthropic or openai)", cfg.Provider, active.Type)
	}
	if !pol.ProviderAllowed(cfg.Provider, active.Type) {
		return nil, badValue(ov.Provider != "", "provider", "%v", pol.Refusal(fmt.Sprintf("provider %q", cfg.Provider)))
	}
	if !pol.ModelAllowed(cfg.Model) {
		return nil, badValue(ov.Model != "", "model", "%v", pol.Refusal(fmt.Sprintf("model %q", cfg.Model)))
	}
	if !llm.ValidEffort(cfg.Effort) {
		return nil, badValue(ov.Effort != "", "effort", "invalid effort %q (want %s)", cfg.Effort, llm.EffortList())
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
			return nil, badValue(ov.Theme != "", "theme", "unknown theme %q (want auto, dark, light or one of: %s)", cfg.Theme, strings.Join(set.Names(), ", "))
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
	if cfg.StatusLine == nil {
		cfg.StatusLine, cfg.StatusLineCompat = cfg.StatusLineCompat, nil
	}
	if cfg.StatusLine != nil {
		if err := cfg.StatusLine.Validate(); err != nil {
			return nil, badf("%s: %v", path, err)
		}
	}
	if _, err := cfg.Permissions.Rules(); err != nil {
		return nil, badf("%s: permissions: %v", path, err)
	}
	km, err := keymap.Resolve(cfg.Keybindings)
	if err != nil {
		return nil, badf("%s: %v", path, err)
	}
	cfg.KeyMap = km
	if err := cfg.LSP.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := cfg.Search.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := validatePrices(cfg.Prices); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := validateSandbox(cfg.Sandbox); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	if err := cfg.Decisions.Validate(); err != nil {
		return nil, badf("%s: %v", path, err)
	}
	// A file that cannot be used beats credentials that are merely
	// missing: /connect can supply a key, not repair a broken theme.
	return cfg, credErr
}

// applyManaged lays the managed policy over a configuration that already
// holds the file, the environment and the command line: its settings
// replace theirs, a disabled --yolo is refused when it was asked for on
// the command line and switched off when the file asked for it, and a
// forced sandbox profile is the project's. A nil policy changes nothing.
func applyManaged(cfg *Config, pol *managed.Policy, ov Overrides) error {
	cfg.Managed = pol
	if pol == nil {
		return nil
	}
	if len(pol.Settings) > 0 {
		obj, err := json.Marshal(pol.Settings)
		if err != nil {
			return badf("managed policy settings: %v", err)
		}
		dec := json.NewDecoder(strings.NewReader(string(obj)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return badf("managed policy %s: settings: %v", strings.Join(pol.Sources, ", "), err)
		}
	}
	if pol.ModeDisabled(managed.ModeYolo) {
		if ov.AutoApprove {
			return badValue(true, "yolo", "%v", pol.Refusal("--yolo"))
		}
		if cfg.AutoApprove {
			cfg.Notes = append(cfg.Notes, "auto_approve is off: the managed policy disables yolo")
		}
		cfg.AutoApprove = false
	}
	if pol.Sandbox != "" {
		cfg.Sandbox = pol.Sandbox
	}
	return nil
}

// validateSandbox accepts an empty value, "off", or a profile name the
// shell package knows. A typo here would otherwise be discovered as a
// session that quietly is not sandboxed.
func validateSandbox(name string) error {
	if name == "" || name == "off" {
		return nil
	}
	if _, ok := shell.FindProfile(name); !ok {
		return fmt.Errorf("sandbox %q is not a profile (want off, %s)", name, strings.Join(shell.ProfileNames(), ", "))
	}
	return nil
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

// FollowupEnabled reports whether ghost-text next-prompt suggestions run.
func (c *Config) FollowupEnabled() bool {
	return c != nil && c.Followup != nil && *c.Followup
}

// ComputerUseEnabled reports whether the Cua Driver MCP server should start.
func (c *Config) ComputerUseEnabled() bool {
	return c != nil && c.ComputerUse != nil && *c.ComputerUse
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
