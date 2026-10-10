package main

import (
	"context"
	"encoding/json"
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
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
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

// firstTurnRepo sends a small repository whole with a fresh session's
// first message (see tool.SmallRepo), every file recorded as read, and
// otherwise leaves the turn to next, the repository map. Only the first
// turn: later ones would carry contents the session has since changed,
// and a resumed session has its reads in its history already.
func firstTurnRepo(ag *agent.Agent, cd *agent.CD, ws *tool.Workspace, seen *tool.Seen, off bool, next func(context.Context, []llm.Block) ([]llm.Block, string)) func(context.Context, []llm.Block) ([]llm.Block, string) {
	return func(ctx context.Context, blocks []llm.Block) ([]llm.Block, string) {
		if !off && len(ag.Messages) == 0 && cd != nil && strings.TrimSpace(firstTextOf(blocks)) != "" {
			if text, files := tool.SmallRepo(ctx, ws, cd.Dir(), seen); text != "" {
				ag.NoteRead(files...)
				return append(blocks, llm.Block{Type: llm.BlockText, Text: text, Synthetic: true}), fmt.Sprintf("repository attached whole: %d files", len(files))
			}
		}
		return next(ctx, blocks)
	}
}

func firstTextOf(blocks []llm.Block) string {
	for _, b := range blocks {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return ""
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

// reachHook is the review tour's measure of how far one file's change
// reaches (see agent.Agent.Reach): the references to every symbol whose
// definition it touches, counted the way the impact line before a
// permission prompt counts them.
func reachHook(cd *agent.CD, diag *lsp.Manager, repo *repomap.Cache) func(ctx context.Context, path, before, after string) int {
	return func(ctx context.Context, path, before, after string) int {
		ix, _ := repo.Get(ctx)
		n := 0
		for _, s := range impact.Analyze(ctx, impact.Input{Path: path, Old: before, New: after, Cwd: cd.Dir(), LSP: diag, RepoIndex: ix}).Symbols {
			n += s.Refs
		}
		return n
	}
}

// agentDeps is what every agent in this process shares: the provider, the
// configuration, the project directory and the process-wide stores. One
// agent is built per session, so an ACP editor opening a second thread
// gets its own history, log and undo state over the same wiring rather
// than a second copy of the wiring.
type agentDeps struct {
	// tools hides tools from the model (--core-tools, --exclude-tools).
	tools toolFilter
	// headless is a run with no one at the terminal (-p, stream-json
	// input): it starts with the coding core only (see stepHeadlessCore).
	headless bool
	// safe is --safe-mode: skills, rules and agent profiles come from the
	// user's configuration directory only, never the project.
	safe    bool
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
	// gate decides how far the project's own skills, agent profiles,
	// commands, rules and executable files are let in (see trust.Gate).
	// Nil lets everything in.
	gate *trust.Gate
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
		{"headless core", b.stepHeadlessCore},
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
	if pol := b.d.cfg.Managed; pol != nil && pol.Sandbox != "" {
		profile = pol.Sandbox
		if err := b.sb.Force(profile, "the managed policy"); err != nil {
			return err
		}
	}
	// The backend and the docker image /sandbox can switch in a session,
	// for a run that has no session to type them into.
	if v := os.Getenv("JANETCODE_SANDBOX_BACKEND"); v != "" {
		if err := b.sb.SetBackend(v); err != nil {
			return fmt.Errorf("JANETCODE_SANDBOX_BACKEND: %w", err)
		}
	}
	if v := os.Getenv("JANETCODE_SANDBOX_IMAGE"); v != "" {
		b.sb.SetImage(v)
	}
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
			tool.WriteFile{Root: d.cwd, WS: b.ws, Seen: b.seen, Cache: b.files, ThenRun: !b.off["thenrun"]},
			tool.EditFile{Root: d.cwd, WS: b.ws, Seen: b.seen, NoLines: b.off["lineedit"], Cache: b.files, ThenRun: !b.off["thenrun"]},
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
	if d.tools.active() {
		ag.Tools.Gate(d.tools.gate)
	}
	// The managed policy: the modes it disables are held off from here
	// on (UpdateSettings clamps them), and the tools it disables are
	// never offered to the model, MCP tools that come up later included.
	ag.Policy = d.cfg.Managed
	ag.UpdateSettings(func(*agent.Settings) {})
	if pol := d.cfg.Managed; pol != nil && len(pol.DisabledTools) > 0 {
		ag.Tools.Disable(pol.ToolDisabled, "disabled by the managed policy")
	}
	// web_search's host is its provider's, not in its input: tell the
	// permission rules, so a host scope covers searching too.
	if searchHost := (tool.WebSearch{Config: d.cfg.Search}).Host(); searchHost != "" {
		perm.RegisterHosts("web_search", func(json.RawMessage) []string { return []string{searchHost} })
	}
	ag.Archive = b.archive
	ag.Root = d.cwd
	ag.Files = b.files
	ag.AdvisorModel = d.cfg.AdvisorModel
	ag.AdvisorMode = d.cfg.AdvisorMode
	ag.AdvisorMaxConsults = d.cfg.AdvisorMaxConsults
	ag.TurnStartStyle = d.cfg.AdvisorTurnStartStyle
	ag.VerifyBaseline = d.cfg.VerifyBaselineEnabled()
	ag.StrongModel = d.cfg.StrongModel
	ag.SetVisionModel(d.cfg.VisionModel)
	ag.SetLanguage(d.cfg.Language)
	settingsapply.ApplyAskWait(d.cfg, ag)
	ag.EscalateAfter = d.cfg.EscalateAfter
	// Every model the session pays for, not only the one it is set to: a
	// fallback, a routed cheap call, an advisor and a subagent each have
	// their own rate, and the cost views add them up per model.
	ag.Prices = d.cfg.PriceFor
	// Their windows likewise: a fallback or a subagent on another model
	// compacts by that model's window, not the session's.
	ag.Windows = func(model string) int { return d.cfg.MetaFor(model).ContextWindow }
	ag.Off = b.off
	if b.d.headless {
		ag.PlanBudget = planBudgetChars
		ag.SpecReview = true
		ag.TestGuard = true
	}
	ag.System = prompt.SystemWith(d.cwd, d.promptOverride)
	ag.Sandbox = b.sb
	ag.Cron = b.cron
	ag.Jobs = d.jobs
	ag.Fallbacks = d.cfg.FallbackModels
	ag.Route = d.models
	ag.RouteCheap = d.cfg.RouteCheap
	ag.StaleCheckNudge = d.cfg.StaleCheckNudge
	ag.EffortControl = d.cfg.EffortControl
	ag.ElideChecks = d.cfg.ElideChecks
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
	gate := b.d.gate
	// --safe-mode is stronger than any trust level: nothing of the
	// project's is read, decided or not.
	project := func() string { return gate.ProjectDir(cd.Dir()) }
	if b.d.safe {
		project = func() string { return "" }
	}
	b.skills = func() []skill.Skill { return skill.WithBuiltins(skill.Load(project(), cfgDir)) }
	b.skillIn = &skill.Injector{Skills: b.skills}
	b.rules = func() []rules.Rule { return rules.Load(project(), cfgDir) }
	b.ruleIn = &rules.Injector{Rules: b.rules}
	ag.PathRules = b.ruleIn.For
	ag.PathSkills = b.skillIn.ForPaths
	b.task.Profiles = func() []skill.Profile { return skill.LoadProfiles(project(), cfgDir) }
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
	// What the orient consult reads (advisor_turn_start_style orient).
	ag.LikelyFiles = func(ctx context.Context, request string) (string, []string) {
		return repomap.Likely(ctx, b.repo, request, 4)
	}
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

// planBudgetChars is how much reasoning a headless reply may stream
// before it acts: about 10k tokens, two to three minutes on MiMo. Past it
// the reply is ended and the model told to act (agent/planbudget.go).
const planBudgetChars = 32_000

// headlessCore is what a headless run's requests carry: the coding loop.
// Everything else stays loadable through tool_search.
var headlessCore = map[string]bool{"read_file": true, "write_file": true, "edit_file": true, "bash": true, "tool_search": true}

// stepHeadlessCore defers every tool but the coding core in a headless
// run. Every request carries the tool list, and a full one is 25 KB —
// four times the system prompt and eight times pi's four tools — for
// tools a run with nobody at the terminal rarely wants (ask, cron,
// workflow, memory, terminal, todo, task, symbols). On hero_agent's
// effbench that was ~6k tokens of input on every call. A deferred tool is
// one tool_search away, and a call to it by name still runs.
// JANETCODE_OFF=headlesscore keeps the full list.
func (b *agentBuilder) stepHeadlessCore() error {
	if !b.d.headless || b.off["headlesscore"] {
		return nil
	}
	advisor := b.d.cfg.AdvisorModel != ""
	b.ag.Tools.DeferAllBut(func(name string) bool {
		return headlessCore[name] || (advisor && name == "advisor")
	})
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
	ag.Reach = reachHook(b.cd, b.diag, b.repo)
	predictor, err := agent.LoadPredictor(filepath.Join(b.d.cfgDir, "critic.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory critic not loaded: %v\n", err)
	}
	ag.Predictor = predictor
	ag.Prepare = chainPrep(skill.TurnPrep(b.skillIn, b.d.mem),
		firstTurnRepo(ag, b.cd, b.ws, b.seen, b.off["inline"], repomap.TurnPrep(b.repo, 0)))
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
// it. Neither is fatal: a project that is not a git repository gets a
// shadow store when it is small enough (see openSnapshots), and an undo
// file that cannot be read costs the session its history of changes, not
// its ability to run.
func (b *agentBuilder) stepUndo() error {
	ag := b.ag
	ag.UndoPath = b.sess.UndoPath()
	ag.Snapshots = openSnapshots(b.d.cwd, b.d.sessDir)
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

// openSnapshots is the snapshot store /undo uses in dir: the repository
// dir is in, or, outside git, a shadow store in janetcode's data
// directory (snapshot.OpenShadow) so a turn's shell changes can be undone
// there too. nil when dir is neither — a home directory, a tree too large
// to snapshot every turn — and /undo falls back to the file tools' copies.
func openSnapshots(dir, sessDir string) *snapshot.Repo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if repo, err := snapshot.Open(ctx, dir); err == nil {
		return repo
	}
	if sessDir == "" {
		return nil
	}
	repo, err := snapshot.OpenShadow(ctx, dir, filepath.Join(filepath.Dir(sessDir), "shadow"))
	if err != nil {
		return nil
	}
	return repo
}
