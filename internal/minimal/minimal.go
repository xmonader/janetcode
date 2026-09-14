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
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

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
func New(root string, cfg *config.Config, prov llm.Provider, off map[string]bool, override prompt.Override, archiveDir string) *agent.Agent {
	repo := &repomap.Cache{Root: root}
	archive := agent.NewArchive(archiveDir)
	expand := &agent.ExpandOutput{Archive: archive}
	ag := &agent.Agent{
		Provider:      prov,
		Off:           off,
		Log:           &session.Memory{},
		Archive:       archive,
		Tools:         tools(root, repo, expand, off),
		System:        prompt.SystemWith(root, override),
		PlanCheck:     repo.CheckPlan,
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		MaxIterations: cfg.MaxIterations,
		CompactAt:     cfg.CompactAt,
		ContextWindow: cfg.MetaFor(cfg.Model).ContextWindow,
		Price:         cfg.PriceFor(cfg.Model),
	}
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
	return ag
}

// tools is the minimal agent's tool set before the tools that close over
// the agent itself (todo, code execution) are added: core file and shell
// tools plus search, the repository map and expand_output. off["extratools"]
// strips it to the four core tools, so the cost of a wide tool surface can
// be measured rather than assumed.
func tools(root string, repo *repomap.Cache, expand *agent.ExpandOutput, off map[string]bool) *tool.Registry {
	core := []tool.Tool{
		tool.ReadFile{Root: root},
		tool.WriteFile{Root: root},
		tool.EditFile{Root: root, NoLines: off["lineedit"]},
		tool.Bash{Root: root, Cache: &tool.ShellCache{}},
	}
	if off["extratools"] {
		return tool.NewRegistry(core...)
	}
	return tool.NewRegistry(append(core,
		tool.ListDir{Root: root}, tool.Glob{Root: root}, tool.Grep{Root: root},
		repomap.Tool{Cache: repo},
		expand,
	)...)
}
