package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/impact"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/weburl"
)

// chainPrep runs turn-preparation hooks in order, joining their notes.
func chainPrep(hooks ...func([]llm.Block) ([]llm.Block, string)) func([]llm.Block) ([]llm.Block, string) {
	return func(blocks []llm.Block) ([]llm.Block, string) {
		var notes []string
		for _, h := range hooks {
			if h == nil {
				continue
			}
			var note string
			blocks, note = h(blocks)
			if note != "" {
				notes = append(notes, note)
			}
		}
		return blocks, strings.Join(notes, " · ")
	}
}

// envOff reads JANETCODE_OFF, a comma-separated list of behaviours to
// disable for this session. An unknown name is dropped: it would switch
// nothing off anyway, and a misspelling in an environment variable should
// not stop a session from starting.
func envOff() map[string]bool {
	off, _ := agent.ParseSwitches(os.Getenv("JANETCODE_OFF"))
	return off
}

func diagnoseNote(m *lsp.Manager) func(context.Context, string) string {
	return func(ctx context.Context, path string) string {
		if m.ServerFor(path) == "" {
			return ""
		}
		lines, ok, err := m.Diagnose(ctx, path)
		switch {
		case err != nil:
			return "[diagnostics unavailable: " + err.Error() + "]"
		case !ok && len(lines) == 0:
			return ""
		case len(lines) == 0:
			return "[diagnostics: no problems (" + m.ServerFor(path) + ")]"
		}
		if len(lines) > 40 {
			lines = append(lines[:40], fmt.Sprintf("… %d more; use the diagnostics tool", len(lines)-40))
		}
		return "[diagnostics]\n" + strings.Join(lines, "\n")
	}
}

// impactHook builds the agent's preview of what a mutating edit reaches
// (see package impact): the language server's resolved view when one
// covers the file, the repository map's index otherwise. edit and write
// reconstruct the exact tools' own apply logic (EditFile.Result,
// WriteFile.Result), so the "after" content analyzed is never out of
// step with what the real call would write. checks is read lazily so it
// sees verify's project config once main has decided whether to trust it,
// even though this hook is wired up before that happens.
func impactHook(cd *agent.CD, ws *tool.Workspace, diag *lsp.Manager, repo *repomap.Cache, checks func() []string) func(context.Context, agent.ToolCall) string {
	edit := tool.EditFile{Root: cd.Dir(), WS: ws}
	write := tool.WriteFile{Root: cd.Dir(), WS: ws}
	return func(ctx context.Context, call agent.ToolCall) string {
		var path, before, after string
		var err error
		switch call.Name {
		case "edit_file":
			path, before, after, err = edit.Result(call.Input)
		case "write_file":
			path, before, after, err = write.Result(call.Input)
		default:
			return ""
		}
		if err != nil || before == "" || before == after {
			// A bad input, a file that does not exist yet (write_file
			// creating one), or an edit that changes nothing: none of
			// these have a reach worth reporting.
			return ""
		}
		ctx, cancel := context.WithTimeout(ctx, impact.Budget)
		defer cancel()
		ix, _ := repo.Get(ctx)
		in := impact.Input{Path: path, Old: before, New: after, Cwd: cd.Dir(), LSP: diag, RepoIndex: ix, Checks: checks()}
		return impact.Analyze(ctx, in).Line()
	}
}

// agentDeps is what every agent in this process shares: the provider, the
// configuration, the project directory and the process-wide stores. One
// agent is built per session, so an ACP editor opening a second thread
// gets its own history, log and undo state over the same wiring rather
// than a second copy of the wiring.
type agentDeps struct {
	prov    llm.Provider
	cfg     *config.Config
	cwd     string
	cfgDir  string
	sessDir string
	mem     *memory.Store
	jobs    *tool.Jobs
	// models resolves a model name to the backend serving it, so a job
	// can be handed to a different model than the session's.
	models   func(string) (llm.Provider, string, error)
	version  string
	readOnly bool
	// autoEdits starts the session with file edits auto-accepted.
	autoEdits bool
	// autoMode starts the session in AUTO: in-workspace edits and
	// known-safe reads skip the approver.
	autoMode bool
	// sandboxOn wraps local bash in docker/sandbox-exec from the start.
	sandboxOn bool
	// promptOverride replaces or extends the system prompt, for an A/B
	// of the prompt itself.
	promptOverride prompt.Override
	// typed holds the hosts the user named themselves. fetch_url reaches a
	// private-network address only for a host that is in here.
	typed *weburl.Set
	// running is the registry of foreground commands that can be moved to
	// the background while they run.
	running *tool.Running
	// extraDirs are additional local roots file tools may use (--add-dir).
	extraDirs []string
	// symbols is the grammar-backed parser the repository map reads
	// non-Go languages with, nil in a build without the grammars. The
	// composition root supplies it; repomap registers nothing itself.
	symbols repomap.Parser
}

// builtAgent is one session's agent and the pieces the caller still owns:
// the subagent tool (its worktrees outlive a turn), the language-server
// manager and the repository map.
type builtAgent struct {
	agent   *agent.Agent
	task    *agent.Task
	diag    *lsp.Manager
	repo    *repomap.Cache
	archive *agent.Archive
	skills  func() []skill.Skill
	// rules lists the project's path-scoped rules; ruleInjector says
	// which of them this session has already sent.
	rules        func() []rules.Rule
	ruleInjector *rules.Injector
	// skillInjector says which skills this session has attached, so
	// /skills can say which of them earned their tokens.
	skillInjector *skill.Injector
	ws            *tool.Workspace
	terms         *tool.Terms
	// cd is the session's working directory and everything rooted at it;
	// /cd moves them together (see agent.CD).
	cd *agent.CD
	// once guards close: the root closes the agent it built, and an
	// error partway through the build closes it again on the way out.
	once *sync.Once
}

// shutdownGrace bounds how long close waits for what it is shutting down.
// A fork wedged in a provider call, or a language server that will not
// answer, must not hold the process open; it is cancelled either way, and
// this is only how long the wait for it lasts.
const shutdownGrace = 5 * time.Second

// close releases what the agent held: the session's scheduled loops, its
// terminals, the subagents it started (cancelled and then awaited, so no
// fork outlives the process that spawned it) and its language servers.
// It is the caller's job, since the pieces outlive any single turn. It is
// safe to call twice and never blocks longer than shutdownGrace.
func (b builtAgent) close() {
	done := func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if b.agent != nil && b.agent.Cron != nil {
			b.agent.Cron.Close()
		}
		if b.terms != nil {
			b.terms.CloseAll()
		}
		// Cancelling the forks and waiting for them comes before the
		// language servers: a fork still running would otherwise go on
		// asking a manager that has already shut down.
		b.task.Close(ctx)
		b.diag.Close()
	}
	if b.once == nil {
		done()
		return
	}
	b.once.Do(done)
}

// buildAgent wires one agent for a session: tools rooted at the project,
// the system prompt, skills and memory, diagnostics, the repository map
// and the trajectory critic.
func buildAgent(d agentDeps, sess *session.Session, history []llm.Message) (builtAgent, error) {
	cd := agent.NewCD(d.cwd)
	task := &agent.Task{Root: d.cwd, Models: d.models}
	jobs := d.jobs
	cfgDir := d.cfgDir
	archive := agent.NewArchive(session.ArchiveDir(d.sessDir, d.cwd))
	mem := d.mem
	seen := &tool.Seen{}
	ws := tool.NewLocal(d.cwd)
	sb := &shell.Sandbox{}
	// The sandbox config key names the profile a session in this project
	// starts under; --sandbox turns one on without naming a profile, and
	// means the default one.
	profile := d.cfg.Sandbox
	if profile == "off" {
		profile = ""
	}
	if profile == "" && d.sandboxOn {
		profile = shell.DefaultProfile
	}
	if profile != "" {
		if err := sb.SetProfile(profile); err != nil {
			return builtAgent{}, err
		}
		if err := sb.Enable(); err != nil {
			return builtAgent{}, fmt.Errorf("sandbox %s: %w", profile, err)
		}
	}
	if jobs != nil {
		jobs.Sandbox = sb
	}
	cron := agent.NewCron()
	if len(sess.Meta.Loops) > 0 {
		if err := cron.Restore(sess.Meta.Loops); err != nil {
			return builtAgent{}, fmt.Errorf("restore loops: %w", err)
		}
	}
	cron.OnChange = func(specs []session.LoopSpec) {
		_ = sess.SetLoops(specs)
	}
	for _, d := range sess.Meta.ExtraDirs {
		_ = ws.AddDir(d)
	}
	for _, d := range d.extraDirs {
		if err := ws.AddDir(d); err != nil {
			return builtAgent{}, fmt.Errorf("--add-dir %s: %w", d, err)
		}
	}
	if extras := ws.ExtraDirs(); len(extras) > 0 {
		_ = sess.SetExtraDirs(extras)
	}
	terms := &tool.Terms{}
	expand := &agent.ExpandOutput{Archive: archive}
	ag, err := agent.New(agent.Deps{
		Provider: d.prov,
		Log:      sess,
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: d.cwd, WS: ws, Seen: seen},
			tool.WriteFile{Root: d.cwd, WS: ws, Seen: seen},
			tool.EditFile{Root: d.cwd, WS: ws, Seen: seen, NoLines: envOff()["lineedit"]},
			tool.Bash{Root: d.cwd, WS: ws, Cache: &tool.ShellCache{}, Jobs: jobs, Running: d.running, Sandbox: sb},
			tool.JobTool{Jobs: jobs},
			tool.TermTool{Terms: terms, Root: d.cwd, WS: ws},
			tool.FetchURL{Typed: d.typed},
			tool.WebSearch{Config: d.cfg.Search},
			expand,
			task,
		),
	}, agent.Settings{
		Model:         d.cfg.Model,
		Price:         d.cfg.PriceFor(d.cfg.Model),
		ContextWindow: d.cfg.MetaFor(d.cfg.Model).ContextWindow,
		MaxTokens:     d.cfg.MaxTokens,
		Effort:        d.cfg.Effort,
		MaxIterations: d.cfg.MaxIterations,
		CompactAt:     d.cfg.CompactAt,
		AutoApprove:   d.cfg.AutoApprove,
		AutoEdits:     d.autoEdits,
		AutoMode:      d.autoMode,
		ReadOnly:      d.readOnly,
		ProofNotes:    d.cfg.ProofNotesEnabled(),
	})
	if err != nil {
		return builtAgent{}, err
	}
	ag.Archive = archive
	ag.Root = d.cwd
	ag.Off = envOff()
	ag.System = prompt.SystemWith(d.cwd, d.promptOverride)
	ag.Sandbox = sb
	ag.Cron = cron
	ag.Messages = history
	ag.Pins = sess.Meta.Pins
	ag.Jobs = jobs
	ag.Fallbacks = d.cfg.FallbackModels
	ag.Route = d.models
	ag.RouteCheap = d.cfg.RouteCheap
	expand.Log = ag.LogEvent // the archive's lookups go on the same trail
	ag.RestoreFromLog()      // plan mode and the turn counter of a resumed session
	ag.SetAccept(sess.Meta.Accept, func(am *session.AcceptMeta) { _ = sess.SetAccept(am) })
	task.Parent = ag
	ag.Tools.Add(&agent.GoalStatus{Parent: ag})
	ag.Tools.Add(&agent.TodoTool{Parent: ag})
	ag.Tools.Add(&agent.AskTool{Parent: ag})
	// JANETCODE_OFF is the ablation switch a session has: the eval runner
	// takes -eval-off, and a real session takes the same names here, so a
	// behaviour can be turned off without a rebuild.
	// execute_code and run_code are one feature: run_code is the
	// Python-less form, offered only where execute_code cannot run, so the
	// model is never asked to choose between two ways to batch tool calls.
	script := &agent.Script{Parent: ag, Root: d.cwd}
	if !envOff()["script"] && script.Available() {
		ag.Tools.Add(script)
	} else {
		script = nil
		ag.Tools.Add(&agent.RunCode{Parent: ag})
	}
	ag.Tools.Add(&agent.Workflow{Parent: ag})
	skills := func() []skill.Skill { return skill.Load(cd.Dir(), cfgDir) }
	skillInj := &skill.Injector{Skills: skills}
	// Path-scoped rules: nothing is sent until a call touches a path a
	// rule names, and then once for the session.
	ruleSet := func() []rules.Rule { return rules.Load(cd.Dir(), cfgDir) }
	injector := &rules.Injector{Rules: ruleSet}
	ag.PathRules = injector.For
	ag.PathSkills = skillInj.ForPaths
	task.Profiles = func() []skill.Profile { return skill.LoadProfiles(cd.Dir(), cfgDir) }
	ag.Tools.Add(&skill.Tool{Skills: skills, Used: skillInj.MarkUsed})
	ag.Tools.Add(&agent.CronTool{Cron: cron})
	ag.Tools.Add(&memory.Tool{Store: mem})
	diag := lsp.New(d.cwd, d.cfg.LSP)
	if diag.Enabled() {
		ag.Tools.Add(lsp.Tool{M: diag})
		ag.Tools.Add(lsp.SymbolTool{M: diag})
		ag.Diagnose = diagnoseNote(diag)
	}
	// The indexes are the part of the system prompt that grows with use
	// rather than with the code, so their share of the window is what is
	// capped, not a flat number of bytes: 8 KB is a twenty-fifth of a 200k
	// window and a sixteenth of a 32k one. Both fall back to the flat
	// ceiling when the window is unknown.
	indexBudget := memory.BudgetFor(ag.ContextWindow)
	ag.SystemExtra = func() string {
		return mem.IndexWithin(indexBudget) + skill.IndexWithin(skills(), indexBudget)
	}
	repo := &repomap.Cache{Root: d.cwd, Parser: d.symbols}
	// The search tools. On a measured fan-out set, offering these
	// alongside bash cost 23% more context (x1.23 [1.10, 1.44]) for the
	// same pass rate, because the model takes many small steps instead of
	// writing one pipeline — see docs/benchmarks/toolsurface. lean_tools
	// defers them behind tool_search, where they stay reachable for the
	// case that measurement does not cover: a large unfamiliar repository.
	search := []tool.Tool{
		tool.ListDir{Root: d.cwd, WS: ws},
		tool.Glob{Root: d.cwd, WS: ws},
		tool.Grep{Root: d.cwd, WS: ws},
		repomap.Tool{Cache: repo},
	}
	for _, t := range search {
		if d.cfg.LeanTools() {
			ag.Tools.AddDeferred(t)
		} else {
			ag.Tools.Add(t)
		}
	}
	ag.PlanCheck = repo.CheckPlan
	// ag.Verify is not wired up yet at this point (main.go sets it once it
	// knows whether the project's verify config is trusted), so the
	// checks are read at call time rather than captured now.
	ag.Impact = impactHook(cd, ws, diag, repo, func() []string {
		if gate, _ := ag.VerifyGate(); gate != nil {
			return gate.Config.Commands
		}
		return nil
	})
	if ag.Predictor, err = agent.LoadPredictor(filepath.Join(cfgDir, "critic.json")); err != nil {
		fmt.Fprintf(os.Stderr, "trajectory critic not loaded: %v\n", err)
	}
	ag.Prepare = chainPrep(skill.TurnPrep(skillInj, mem), repomap.TurnPrep(repo, 0))
	ag.Tools.Intent = d.cfg.IntentEnabled() && !envOff()["intent"]
	ag.Tools.Add(&tool.ToolSearch{Registry: ag.Tools, ContextWindow: ag.ContextWindow})
	ag.UndoPath = sess.UndoPath()
	if snaps, serr := snapshot.Open(context.Background(), d.cwd); serr == nil {
		ag.Snapshots = snaps
	}
	if err := ag.LoadUndo(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	cd.Bind(ws, ag, sess)
	registerRooted(cd, rootedDeps{d: d, ag: ag, task: task, script: script, diag: diag, repo: repo, mem: mem, terms: terms, jobs: jobs, running: d.running, skills: skills, rules: ruleSet})

	return builtAgent{once: &sync.Once{}, agent: ag, task: task, diag: diag, repo: repo, archive: archive, skills: skills, rules: ruleSet, ruleInjector: injector, skillInjector: skillInj, ws: ws, terms: terms, cd: cd}, nil
}
