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
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/weburl"
)

// chainPrep runs turn-preparation hooks in order, joining their notes.
// Each is given the turn's context, so cancelling the turn cancels the
// preparation with it.
func chainPrep(hooks ...func(context.Context, []llm.Block) ([]llm.Block, string)) func(context.Context, []llm.Block) ([]llm.Block, string) {
	return func(ctx context.Context, blocks []llm.Block) ([]llm.Block, string) {
		var notes []string
		for _, h := range hooks {
			if h == nil {
				continue
			}
			var note string
			blocks, note = h(ctx, blocks)
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
	notebook := tool.NotebookEdit{Root: cd.Dir(), WS: ws}
	return func(ctx context.Context, call agent.ToolCall) string {
		var path, before, after string
		var err error
		switch call.Name {
		case "edit_file":
			path, before, after, err = edit.Result(ctx, call.Input)
		case "write_file":
			path, before, after, err = write.Result(ctx, call.Input)
		case "notebook_edit":
			path, before, after, err = notebook.Result(ctx, call.Input)
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

// sandboxProfile decides which sandbox profile a session starts under.
// The config key names the profile a session in this project starts with;
// "off" is the explicit opt-out and beats --sandbox, which only asks for
// "a sandbox" and so means the default profile. "" is no sandbox.
func sandboxProfile(configured string, sandboxFlag bool) string {
	if configured == "off" {
		return ""
	}
	if configured != "" {
		return configured
	}
	if sandboxFlag {
		return shell.DefaultProfile
	}
	return ""
}

// agentBuilder assembles one session's agent in named steps. It exists
// because the wiring is long, order-dependent and hard to see: every
// field below is produced by one step and consumed by a later one, so
// the dependencies are written down rather than implied by the order of
// two hundred statements. Each step is a method that can be run on its
// own in a test with whatever pieces it actually needs.
type agentBuilder struct {
	d       agentDeps
	sess    *session.Session
	history []llm.Message
	// off is JANETCODE_OFF read once. It was read four times, which meant
	// a session could in principle disagree with itself about what is
	// switched off.
	off map[string]bool

	// Produced by the steps, in the order they run.
	cd      *agent.CD
	task    *agent.Task
	archive *agent.Archive
	ws      *tool.Workspace
	seen    *tool.Seen
	files   *tool.IOCache
	sb      *shell.Sandbox
	cron    *agent.Cron
	terms   *tool.Terms
	expand  *agent.ExpandOutput
	ag      *agent.Agent
	script  *agent.Script
	diag    *lsp.Manager
	repo    *repomap.Cache
	skills  func() []skill.Skill
	rules   func() []rules.Rule
	skillIn *skill.Injector
	ruleIn  *rules.Injector
}

func newAgentBuilder(d agentDeps, sess *session.Session, history []llm.Message) *agentBuilder {
	return &agentBuilder{
		d: d, sess: sess, history: history, off: envOff(),
		cd:      agent.NewCD(d.cwd),
		task:    &agent.Task{Root: d.cwd, Models: d.models},
		archive: agent.NewArchive(session.ArchiveDir(d.sessDir, d.cwd)),
		ws:      tool.NewLocal(d.cwd),
		seen:    &tool.Seen{},
		files:   &tool.IOCache{},
		sb:      &shell.Sandbox{},
		terms:   &tool.Terms{},
	}
}

// buildSteps is the build, in order. Naming the steps means a failure
// says which part of the wiring refused rather than only what it said.
func (b *agentBuilder) buildSteps() []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"sandbox", b.stepSandbox},
		{"workspace", b.stepWorkspace},
		{"loops", b.stepLoops},
		{"core", b.stepCore},
		{"session state", b.stepSessionState},
		{"agent tools", b.stepAgentTools},
		{"knowledge", b.stepKnowledge},
		{"diagnostics", b.stepDiagnostics},
		{"search tools", b.stepSearchTools},
		{"turn hooks", b.stepTurnHooks},
		{"decisions", b.stepDecisions},
		{"undo", b.stepUndo},
	}
}

// stepSandbox turns on the shell sandbox this session runs under, if any,
// and hands it to the background job runner as well: a job started from a
// sandboxed session is sandboxed too.
func (b *agentBuilder) stepSandbox() error {
	profile := sandboxProfile(b.d.cfg.Sandbox, b.d.sandboxOn)
	if profile != "" {
		if err := b.sb.SetProfile(profile); err != nil {
			return err
		}
		if err := b.sb.Enable(); err != nil {
			return fmt.Errorf("sandbox %s: %w", profile, err)
		}
	}
	if b.d.jobs != nil {
		b.d.jobs.Sandbox = b.sb
	}
	return nil
}

// stepWorkspace settles which local roots the file tools may touch: the
// project, whatever the session already recorded, and this run's
// --add-dir. A directory the session recorded but that has since gone is
// dropped silently; one named on this command line is an error, because
// the user just asked for it.
func (b *agentBuilder) stepWorkspace() error {
	for _, dir := range b.sess.Meta.ExtraDirs {
		_ = b.ws.AddDir(dir)
	}
	for _, dir := range b.d.extraDirs {
		if err := b.ws.AddDir(dir); err != nil {
			return fmt.Errorf("--add-dir %s: %w", dir, err)
		}
	}
	if extras := b.ws.ExtraDirs(); len(extras) > 0 {
		_ = b.sess.SetExtraDirs(extras)
	}
	return nil
}

// stepLoops restores the session's armed /loop jobs and keeps the session
// record in step with them from here on.
func (b *agentBuilder) stepLoops() error {
	b.cron = agent.NewCron()
	if len(b.sess.Meta.Loops) > 0 {
		if err := b.cron.Restore(b.sess.Meta.Loops); err != nil {
			return fmt.Errorf("restore loops: %w", err)
		}
	}
	sess := b.sess
	b.cron.OnChange = func(specs []session.LoopSpec) { _ = sess.SetLoops(specs) }
	return nil
}

// stepCore builds the agent itself: the provider, the settings the
// configuration and this run's flags settle on, and the tools that are
// plain functions of the project directory.
func (b *agentBuilder) stepCore() error {
	d := b.d
	b.expand = &agent.ExpandOutput{Archive: b.archive}
	ag, err := agent.New(agent.Deps{
		Provider: d.prov,
		Log:      b.sess,
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: d.cwd, WS: b.ws, Seen: b.seen, Cache: b.files},
			tool.WriteFile{Root: d.cwd, WS: b.ws, Seen: b.seen, Cache: b.files},
			tool.EditFile{Root: d.cwd, WS: b.ws, Seen: b.seen, NoLines: b.off["lineedit"], Cache: b.files},
			tool.Bash{Root: d.cwd, WS: b.ws, Cache: &tool.ShellCache{}, Jobs: d.jobs, Running: d.running, Sandbox: b.sb},
			tool.JobTool{Jobs: d.jobs},
			tool.TermTool{Terms: b.terms, Root: d.cwd, WS: b.ws},
			tool.FetchURL{Typed: d.typed},
			tool.WebSearch{Config: d.cfg.Search},
			b.expand,
			b.task,
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
		return err
	}
	b.ag = ag
	ag.Archive = b.archive
	ag.Root = d.cwd
	ag.Files = b.files
	ag.AdvisorModel = d.cfg.AdvisorModel
	ag.AdvisorMode = d.cfg.AdvisorMode
	ag.AdvisorMaxConsults = d.cfg.AdvisorMaxConsults
	ag.StrongModel = d.cfg.StrongModel
	ag.SetVisionModel(d.cfg.VisionModel)
	ag.EscalateAfter = d.cfg.EscalateAfter
	// Every model the session pays for, not only the one it is set to: a
	// fallback, a routed cheap call, an advisor and a subagent each have
	// their own rate, and the cost views add them up per model.
	ag.Prices = d.cfg.PriceFor
	// Their windows likewise: a fallback or a subagent on another model
	// compacts by that model's window, not the session's.
	ag.Windows = func(model string) int { return d.cfg.MetaFor(model).ContextWindow }
	ag.Off = b.off
	ag.System = prompt.SystemWith(d.cwd, d.promptOverride)
	ag.Sandbox = b.sb
	ag.Cron = b.cron
	ag.Jobs = d.jobs
	ag.Fallbacks = d.cfg.FallbackModels
	ag.Route = d.models
	ag.RouteCheap = d.cfg.RouteCheap
	ag.StaleCheckNudge = d.cfg.StaleCheckNudge
	b.expand.Log = ag.LogEvent // the archive's lookups go on the same trail
	b.task.Parent = ag
	return nil
}

// stepSessionState puts back what a resumed session already knows: its
// history, its pins, its plan mode and turn counter, and its /accept
// lock.
func (b *agentBuilder) stepSessionState() error {
	ag, sess := b.ag, b.sess
	ag.Messages = b.history
	ag.Pins = sess.Meta.Pins
	ag.RestoreFromLog() // plan mode and the turn counter of a resumed session
	ag.SetAccept(sess.Meta.Accept, func(am *session.AcceptMeta) { _ = sess.SetAccept(am) })
	return nil
}

// stepAgentTools adds the tools that need the agent itself: the ones that
// read or drive its state, and the two batching tools that are one
// feature between them.
//
// JANETCODE_OFF is the ablation switch a session has: the eval runner
// takes -eval-off, and a real session takes the same names here, so a
// behaviour can be turned off without a rebuild. execute_code and
// run_code are one feature: run_code is the Python-less form, offered
// only where execute_code cannot run, so the model is never asked to
// choose between two ways to batch tool calls.
func (b *agentBuilder) stepAgentTools() error {
	ag := b.ag
	ag.Tools.Add(&agent.GoalStatus{Parent: ag})
	ag.Tools.Add(&agent.TodoTool{Parent: ag})
	ag.Tools.Add(&agent.AskTool{Parent: ag})
	script := &agent.Script{Parent: ag, Root: b.d.cwd}
	if !b.off["script"] && script.Available() {
		b.script = script
		ag.Tools.Add(script)
	} else {
		ag.Tools.Add(&agent.RunCode{Parent: ag})
	}
	ag.Tools.Add(&agent.Workflow{Parent: ag})
	ag.Tools.Add(&agent.CronTool{Cron: b.cron})
	ag.Tools.Add(&memory.Tool{Store: b.d.mem})
	return nil
}

// stepKnowledge wires the three things that are read from the project
// rather than from the model: skills, path-scoped rules and memory.
// Nothing is sent until it is earned — a rule when a call touches a path
// it names, a skill when the injector picks it — and then once for the
// session.
func (b *agentBuilder) stepKnowledge() error {
	ag, cd, cfgDir, mem := b.ag, b.cd, b.d.cfgDir, b.d.mem
	b.skills = func() []skill.Skill { return skill.WithBuiltins(skill.Load(cd.Dir(), cfgDir)) }
	b.skillIn = &skill.Injector{Skills: b.skills}
	b.rules = func() []rules.Rule { return rules.Load(cd.Dir(), cfgDir) }
	b.ruleIn = &rules.Injector{Rules: b.rules}
	ag.PathRules = b.ruleIn.For
	ag.PathSkills = b.skillIn.ForPaths
	b.task.Profiles = func() []skill.Profile { return skill.LoadProfiles(cd.Dir(), cfgDir) }
	ag.Tools.Add(&skill.Tool{Skills: b.skills, Used: b.skillIn.MarkUsed})
	// The indexes are the part of the system prompt that grows with use
	// rather than with the code, so their share of the window is what is
	// capped, not a flat number of bytes: 8 KB is a twenty-fifth of a 200k
	// window and a sixteenth of a 32k one. Both fall back to the flat
	// ceiling when the window is unknown.
	indexBudget := memory.BudgetFor(ag.ContextWindow)
	mem.PlainRanking = b.off["memoryrank"]
	skills := b.skills
	ag.SystemExtra = func() string {
		return mem.IndexWithin(indexBudget) + skill.IndexWithin(skills(), indexBudget)
	}
	return nil
}

// stepDiagnostics starts the language-server manager, when the config
// leaves it on, and gives the agent the per-edit diagnostics note.
func (b *agentBuilder) stepDiagnostics() error {
	b.diag = lsp.New(b.d.cwd, b.d.cfg.LSP)
	if b.diag.Enabled() {
		b.ag.Tools.Add(lsp.Tool{M: b.diag})
		b.ag.Tools.Add(lsp.SymbolTool{M: b.diag})
		b.ag.Diagnose = diagnoseNote(b.diag)
	}
	return nil
}

// stepSearchTools adds the retrieval surface. On a measured fan-out set,
// offering the search tools alongside bash cost 23% more context
// (x1.23 [1.10, 1.44]) for the same pass rate, because the model takes
// many small steps instead of writing one pipeline — see
// docs/benchmarks/toolsurface. lean_tools defers them behind tool_search,
// where they stay reachable for the case that measurement does not cover:
// a large unfamiliar repository. The extra retrieval and desktop tools
// stay deferred either way, so they cannot eat the prefix the leanness
// ceiling is guarding.
func (b *agentBuilder) stepSearchTools() error {
	ag, d := b.ag, b.d
	b.repo = &repomap.Cache{Root: d.cwd, Parser: d.symbols}
	for _, t := range []tool.Tool{
		tool.ListDir{Root: d.cwd, WS: b.ws},
		tool.Glob{Root: d.cwd, WS: b.ws},
		tool.Grep{Root: d.cwd, WS: b.ws},
		repomap.Tool{Cache: b.repo},
	} {
		if d.cfg.LeanTools() {
			ag.Tools.AddDeferred(t)
		} else {
			ag.Tools.Add(t)
		}
	}
	ag.Tools.AddDeferred(tool.ReadManyFiles{Root: d.cwd, WS: b.ws, Seen: b.seen, Cache: b.files})
	ag.Tools.AddDeferred(tool.Computer{Root: d.cwd})
	ag.Tools.AddDeferred(&agent.AdvisorTool{Parent: ag})
	// Notebooks are rare, so their edit tool is not in every request.
	// read_file's rendering of a notebook names it, and tool_search loads
	// it: that returns the schema in the same turn, which activating it
	// from read_file could not (a turn's tool list is fixed when the turn
	// starts, so the model was told to use a tool it had no schema for).
	ag.Tools.AddDeferred(tool.NotebookEdit{Root: d.cwd, WS: b.ws, Seen: b.seen, Cache: b.files})
	ag.Tools.AddDeferred(tool.Monitor{Jobs: d.jobs, Root: d.cwd, WS: b.ws, OnMatch: ag.Steer})
	if d.cfg.AdvisorModel != "" {
		// Opting into an advisor model is the experiment being on: the
		// executor has to see the tool without a tool_search hunt.
		ag.Tools.Activate("advisor")
	}
	ag.Tools.Intent = d.cfg.IntentEnabled() && !b.off["intent"]
	ag.Tools.Add(&tool.ToolSearch{Registry: ag.Tools, ContextWindow: ag.ContextWindow})
	return nil
}

// stepTurnHooks wires what runs around a turn rather than inside it: the
// plan check, the impact preview, the trajectory critic and the
// turn-preparation chain. A critic that will not load is a note on
// stderr, not a session that refuses to start.
func (b *agentBuilder) stepTurnHooks() error {
	ag := b.ag
	ag.PlanCheck = b.repo.CheckPlan
	// ag.Verify is not wired up yet at this point (wireProject sets it
	// once it knows whether the project's verify config is trusted), so
	// the checks are read at call time rather than captured now.
	ag.Impact = impactHook(b.cd, b.ws, b.diag, b.repo, func() []string {
		if gate, _ := ag.VerifyGate(); gate != nil {
			return gate.Config.Commands
		}
		return nil
	})
	predictor, err := agent.LoadPredictor(filepath.Join(b.d.cfgDir, "critic.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory critic not loaded: %v\n", err)
	}
	ag.Predictor = predictor
	ag.Prepare = chainPrep(skill.TurnPrep(b.skillIn, b.d.mem), repomap.TurnPrep(b.repo, 0))
	return nil
}

// stepDecisions wires the optional TypeSafe Jev decision client
// (internal/jev) when the config's "decisions" block asks for it. The
// client is advisory by contract and nothing here can fail the build: a
// missing key is a note on stderr and a session without the feature.
func (b *agentBuilder) stepDecisions() error {
	if settingsapply.WireDecisions(b.d.cfg, b.ag, b.off) {
		fmt.Fprintf(os.Stderr, "decisions: configured but %s is empty; off for this session\n", b.d.cfg.Decisions.KeyEnv())
	}
	return nil
}

// stepUndo opens the session's undo ledger and the snapshot store behind
// it. Neither is fatal: a project that is not a git repository has no
// snapshots, and an undo file that cannot be read costs the session its
// history of changes, not its ability to run.
func (b *agentBuilder) stepUndo() error {
	ag := b.ag
	ag.UndoPath = b.sess.UndoPath()
	if snaps, err := snapshot.Open(context.Background(), b.d.cwd); err == nil {
		ag.Snapshots = snaps
	}
	if err := ag.LoadUndo(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	return nil
}

// result binds the /cd tracker to everything rooted at the project and
// hands back what the caller still owns.
func (b *agentBuilder) result() builtAgent {
	b.cd.Bind(b.ws, b.ag, b.sess)
	registerRooted(b.cd, rootedDeps{d: b.d, ag: b.ag, task: b.task, script: b.script, diag: b.diag,
		repo: b.repo, mem: b.d.mem, terms: b.terms, jobs: b.d.jobs, running: b.d.running,
		skills: b.skills, rules: b.rules})
	return builtAgent{
		once: &sync.Once{}, agent: b.ag, task: b.task, diag: b.diag, repo: b.repo,
		archive: b.archive, skills: b.skills, rules: b.rules, ruleInjector: b.ruleIn,
		skillInjector: b.skillIn, ws: b.ws, terms: b.terms, cd: b.cd,
	}
}

// buildAgent wires one agent for a session: tools rooted at the project,
// the system prompt, skills and memory, diagnostics, the repository map
// and the trajectory critic. The work is agentBuilder's, one named step
// at a time; this is the order they run in and what a failure in one of
// them is reported as.
func buildAgent(d agentDeps, sess *session.Session, history []llm.Message) (builtAgent, error) {
	b := newAgentBuilder(d, sess, history)
	for _, step := range b.buildSteps() {
		if err := step.fn(); err != nil {
			return builtAgent{}, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return b.result(), nil
}
