// Package minimal builds the "minimal agent" that evals (cmd/janetcode's
// --eval) and counterfactual replay (internal/whatif) run: the real,
// not read-only, core tools of a session rooted at one directory, plus
// search, the repository map, expand_output, todo and code execution —
// and no MCP, hooks, memory, skills, terminal or network tools (see
// docs/manual/evals.md). A lean, well-understood surface makes a result
// about the model, the prompt or the change under test rather than about
// which product features happened to be wired up.
//
// It is its own package so both callers share one builder without
// internal/whatif importing internal/eval.
package minimal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// Option adjusts how New builds the agent.
type Option func(*options)

type options struct{ profile string }

// WithSandbox runs the agent's bash under the named sandbox profile
// (workspace, read-only, strict; see internal/shell/profile.go) instead of
// on the bare host. Empty leaves bash unsandboxed, the default. New fails
// when the machine has no backend that can apply the profile, rather than
// quietly running unconfined. See Sandbox for the extra write dirs a
// profile that confines writes gets.
func WithSandbox(profile string) Option { return func(o *options) { o.profile = profile } }

// Sandbox builds the enabled sandbox for profile and the workspace that
// carries its extra write dirs. A profile that confines writes also makes
// `go env GOCACHE` and `go env GOMODCACHE` writable, when go is on PATH:
// without the first every go build starts from a cold cache, and go takes
// lock files in the second even when every module is already downloaded
// (a read-only module cache fails with "read-only file system"). With no
// network, nothing new can be fetched into it.
// JANETCODE_SANDBOX_BACKEND picks a backend as it does for a session.
func Sandbox(root, profile string) (*shell.Sandbox, *tool.Workspace, error) {
	sb := &shell.Sandbox{}
	if err := sb.SetProfile(profile); err != nil {
		return nil, nil, err
	}
	if v := os.Getenv("JANETCODE_SANDBOX_BACKEND"); v != "" {
		if err := sb.SetBackend(v); err != nil {
			return nil, nil, fmt.Errorf("JANETCODE_SANDBOX_BACKEND: %w", err)
		}
	}
	if err := sb.Enable(); err != nil {
		return nil, nil, fmt.Errorf("sandbox %s: %w", profile, err)
	}
	ws := tool.NewLocal(root)
	if slicesContains(sb.Profile().WriteRoots, shell.WriteExtraToken) {
		for _, dir := range goDirs() {
			if err := os.MkdirAll(dir, 0o755); err == nil {
				_ = ws.AddDir(dir)
			}
		}
	}
	return sb, ws, nil
}

func slicesContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// goDirs are the go build cache and module cache directories, or nil
// when go is not installed or does not say.
func goDirs() []string {
	path, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "env", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, d := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if d = strings.TrimSpace(d); d != "" && d != "off" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// New returns a minimal agent rooted at root. Model, token, iteration,
// compaction, context-window and price settings come from cfg; off
// ablates named behaviours (see agent.ParseSwitches); override replaces
// or extends the system prompt; archiveDir is where tool outputs are
// archived for expand_output. The caller sets anything run-specific
// afterwards (AutoApprove, Rules, Route, a different MaxIterations).
//
// Code execution follows a real session (cmd/janetcode's buildAgent):
// execute_code when its runtime is available and not switched off,
// run_code otherwise, so the agent always has one of the two.
// symbols, when not nil, is the grammar-backed parser the repository map
// reads non-Go languages with (see internal/treesitter); the caller is
// the composition root, so nothing here reaches for a package global.
func New(root string, cfg *config.Config, prov llm.Provider, off map[string]bool, override prompt.Override, archiveDir string, symbols repomap.Parser, opts ...Option) (*agent.Agent, error) {
	var o options
	for _, f := range opts {
		f(&o)
	}
	var sb *shell.Sandbox
	var ws *tool.Workspace
	if o.profile != "" {
		var err error
		if sb, ws, err = Sandbox(root, o.profile); err != nil {
			return nil, err
		}
	}
	repo := &repomap.Cache{Root: root, Parser: symbols}
	archive := agent.NewArchive(archiveDir)
	expand := &agent.ExpandOutput{Archive: archive}
	ag, err := agent.New(agent.Deps{
		Provider: prov,
		Log:      &session.Memory{},
		Tools:    tools(root, repo, expand, off, ws, sb),
	}, agent.Settings{
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		MaxIterations: cfg.MaxIterations,
		CompactAt:     cfg.CompactAt,
		ContextWindow: cfg.MetaFor(cfg.Model).ContextWindow,
		Price:         cfg.PriceFor(cfg.Model),
	})
	if err != nil {
		return nil, err
	}
	ag.Prices = cfg.PriceFor
	ag.Spend = cfg.Spend.Guard(os.Getenv, nil)
	ag.Windows = func(model string) int { return cfg.MetaFor(model).ContextWindow }
	// The strong slot and the cascade: an eval arm that measures
	// escalation needs the same wiring a session has, and both are inert
	// until the config names a model and a round count.
	ag.StrongModel = cfg.StrongModel
	ag.EscalateAfter = cfg.EscalateAfter
	ag.AdvisorModel = cfg.AdvisorModel
	ag.AdvisorMode = cfg.AdvisorMode
	ag.AdvisorMaxConsults = cfg.AdvisorMaxConsults
	ag.TurnStartStyle = cfg.AdvisorTurnStartStyle
	ag.VerifyBaseline = cfg.VerifyBaselineEnabled()
	ag.LikelyFiles = func(ctx context.Context, request string) (string, []string) {
		return repomap.Likely(ctx, repo, request, 4)
	}
	ag.Off = off
	ag.Archive = archive
	ag.System = prompt.SystemWith(root, override)
	ag.PlanCheck = repo.CheckPlan
	expand.Log = ag.LogEvent // the archive's lookups go on the same trail
	if !off["repomap"] {
		ag.Prepare = repomap.TurnPrep(repo, 0)
	}
	if !off["todo"] {
		ag.Tools.Add(&agent.TodoTool{Parent: ag})
	}
	if script := (&agent.Script{Parent: ag, Root: root}); !off["script"] && script.Available() {
		ag.Tools.Add(script)
	} else {
		ag.Tools.Add(&agent.RunCode{Parent: ag})
	}
	// Intent follows the product default, so a measurement is of what
	// users actually run; off["intent"] forces it off.
	ag.Tools.Intent = cfg.IntentEnabled() && !off["intent"]
	return ag, nil
}

// tools is the minimal agent's tool set before the tools that close over
// the agent itself (todo, code execution) are added: core file and shell
// tools plus search, the repository map and expand_output. off["extratools"]
// strips it to the four core tools, so the cost of a wide tool surface can
// be measured rather than assumed.
// The empty-search probe and the bash hint are wired as a session wires
// them (agent_build.go): without them an A/B of emptyprobe or toolnudge
// compared two identical arms.
func tools(root string, repo *repomap.Cache, expand *agent.ExpandOutput, off map[string]bool, ws *tool.Workspace, sb *shell.Sandbox) *tool.Registry {
	var probe *tool.EmptyProbe
	if !off["emptyprobe"] {
		probe = &tool.EmptyProbe{}
	}
	var reg *tool.Registry
	var nudge *tool.Nudge
	if !off["toolnudge"] {
		nudge = &tool.Nudge{Loaded: func(name string) bool { return reg.Loaded(name) }}
	}
	core := []tool.Tool{
		tool.ReadFile{Root: root},
		tool.WriteFile{Root: root},
		tool.EditFile{Root: root, NoLines: off["lineedit"]},
		tool.Bash{Root: root, WS: ws, Sandbox: sb, Cache: &tool.ShellCache{}, Probe: probe, Nudge: nudge},
	}
	if off["extratools"] {
		reg = tool.NewRegistry(core...)
		return reg
	}
	reg = tool.NewRegistry(append(core,
		tool.ListDir{Root: root}, tool.Glob{Root: root}, tool.Grep{Root: root, Probe: probe},
		repomap.Tool{Cache: repo},
		expand,
	)...)
	return reg
}
