// What a front-end hands the terminal session: Options, the per-subject
// option groups it carries, and Run, the only entry point.
package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/theme"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/weburl"
)

// Options configures the UI.
type Options struct {
	// Notes are decisions the configuration made on the user's behalf
	// (a provider switched for one that has a key); shown once at start.
	Notes []string
	// ConfigDir holds global custom commands under commands/*.md.
	ConfigDir string
	// MCP, when set, describes the servers to start.
	MCP   *MCPOptions
	Hooks *HookOptions
	Perms *PermOptions
	// Verify carries the project's verification commands (/verify).
	Verify *VerifyOptions
	// Rules says which path-scoped rules this session has already given
	// the model, for /rules to mark them.
	Rules *rules.Injector
	// Skills says which skills this session has attached and what they
	// cost, for /skills to price them.
	Skills *skill.Injector
	// LSP reports language-server status (/lsp).
	LSP *lsp.Manager
	// Inspect answers for every part of the session; /inspect reads it.
	Inspect *inspect.Registry
	// Features is the catalogue of what janetcode can do; /help <topic>
	// searches it and the embedded manual. Nil leaves /help as it was.
	Features *feature.Catalog
	// Usage counts which catalogue features the user has reached for, so
	// hints can skip what they already know and /stats can name what they
	// have not tried. It stays on this machine; nil disables counting.
	Usage *feature.Usage
	// Hints is the one-time hint engine. Nil, or an engine with Off set,
	// means the session says nothing beyond what it is asked.
	Hints *hints.Engine
	// Untrusted names project files that are present but waiting for
	// approval (.mcp.json and friends), for the hint that says so.
	Untrusted []string
	// UnpricedModel is the configured model when nothing can price it.
	UnpricedModel string
	// Models resolves a model name to the backend serving it, so /model
	// can follow a name to another provider.
	Models func(string) (llm.Provider, string, error)
	// Jobs are background commands; finished ones are announced while idle.
	Jobs *tool.Jobs
	// Running is the registry of foreground commands that can be moved to
	// the background (ctrl+g). Nil disables the key.
	Running *tool.Running
	// Typed collects the hosts the user names in their own messages;
	// fetch_url reaches a private address only for one of them.
	Typed *weburl.Set
	// AutoTitle asks the model for a short session title after the first turn.
	AutoTitle bool
	// Doctor writes the same checkup janetcode --doctor prints. Nil
	// leaves /doctor as a short local note.
	Doctor   func(io.Writer) error
	Agent    *agent.Agent
	Session  *session.Session
	Provider string
	// Config, ConfigPath and Build enable /connect and the /model picker:
	// Build constructs a backend for a config, Update persists the choice.
	Config     *config.Config
	ConfigPath string
	Build      func(*config.Config) (llm.Provider, error)
	// Connect opens the provider dialog at start because Agent.Provider is
	// nil: nothing is configured yet.
	Connect bool
	Cwd     string
	// Workspace is the shared host the file and shell tools use. /ssh
	// and /docker retarget it in place. Nil keeps tools on Cwd.
	Workspace *tool.Workspace
	// CD is the session's working directory and everything rooted at it;
	// /cd moves them together. Nil disables /cd.
	CD      *agent.CD
	Resumed int // messages loaded from a resumed session
	Version string
	// Theme is "auto"/"" (detect), "dark", "light", or the name of a
	// builtin or user theme.
	Theme string
	// Themes is the resolved, validated set of builtins plus user themes,
	// from config. Nil falls back to the builtins alone.
	Themes *theme.Set
	// InitialPrompt is sent as soon as the UI is ready.
	InitialPrompt string
	// Memory is the project's memory store; AutoMemory extracts facts
	// after turns that changed something.
	Memory     *memory.Store
	AutoMemory bool
}

// Run starts the interactive UI and blocks until the user quits.
func Run(opts Options) error {
	m := newModel(opts)
	defer m.endLife()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	m.send = p.Send
	m.queue.send = p.Send
	m.approver.send = p.Send
	m.asker.send = p.Send
	opts.Agent.Approver = m.approver
	opts.Agent.Asker = m.asker
	_, err := p.Run()
	return err
}

// MCPOptions lists the servers from the config file (started right away)
// and from the project's .mcp.json (started once the user trusts it).
type MCPOptions struct {
	Manager        *mcptool.Manager
	Global         map[string]mcp.ServerConfig
	Project        map[string]mcp.ServerConfig
	ProjectHash    string
	ProjectTrusted bool
	Trust          trust.Store
	// Tokens stores OAuth tokens for /mcp auth; nil disables it.
	Tokens *mcp.TokenStore
	// OpenURL opens the browser for /mcp auth; nil uses mcp.OpenBrowser.
	OpenURL func(string) error
}

// HookOptions describes the hooks in effect for /hooks.
type HookOptions struct {
	Global         hook.Config
	Project        hook.Config
	ProjectHash    string
	ProjectTrusted bool
	Trust          trust.Store
}

// PermOptions describes the permission rules in effect for /permissions.
type PermOptions struct {
	Project        perm.Rules
	ProjectHash    string
	ProjectTrusted bool
	Trust          trust.Store
	// Save persists a rule to the config file.
	Save func(kind string, r perm.Rule) error
}
