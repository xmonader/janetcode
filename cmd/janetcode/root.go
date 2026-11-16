package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/xmonader/janetcode/internal/extension"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/treesitter"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/verify"
	"github.com/xmonader/janetcode/internal/weburl"
)

// root is the composition root's state: everything this process built for
// one session, held in one place so the modes that can end a run --
// --inspect, --recipe, --acp, a headless prompt, --web and the TUI -- all
// read the same wiring instead of each assembling its own.
type root struct {
	opts    *options
	fs      *flag.FlagSet
	cfg     *config.Config
	prov    llm.Provider
	cwd     string
	cfgPath string
	cfgDir  string
	sessDir string
	initial string
	format  headless.Format
	// needsConnect is the interactive first run: the TUI opens on
	// /connect rather than the process failing for want of a key.
	needsConnect bool

	sess    *session.Session
	history []llm.Message
	deps    agentDeps
	built   builtAgent
	ag      *agent.Agent
	diag    *lsp.Manager
	archive *agent.Archive
	skills  func() []skill.Skill

	jobs     *tool.Jobs
	mem      *memory.Store
	pool     *providerPool
	typed    *weburl.Set
	servers  mcptool.Manager
	acpAsker agent.Asker // set in ACP mode: elicitation goes to the editor
	subjects *inspect.Registry
	catalog  *feature.Catalog
	usage    *feature.Usage
	hints    *hints.Engine

	trust          trust.Store
	gate           *trust.Gate
	tokens         *mcp.TokenStore
	projectServers map[string]mcp.ServerConfig
	projectHash    string
	projectTrusted bool
	hooksCfg       hook.Config
	hooksHash      string
	hooksTrusted   bool
	verifyCfg      verify.Config
	verifyHash     string
	verifyDefined  bool
	verifyTrusted  bool
	projRules      perm.Rules
	permHash       string
	permTrusted    bool
	// uiHooks, uiVerify and uiPerms are the front-end's view of the
	// project's own files, held here so /cd can re-point them at the
	// directory it moved to. They are set by the mode that builds them
	// and stay nil in the modes that do not.
	uiHooks  *tui.HookOptions
	uiVerify *tui.VerifyOptions
	uiPerms  *tui.PermOptions

	// closers are run in reverse on the way out, the order the defers
	// they replace ran in.
	closers []func()
}

func (r *root) onClose(f func()) { r.closers = append(r.closers, f) }

// close releases everything the root started, newest first: the feature
// counters, the debug log, the MCP servers, the agent (its subagents,
// loops, terminals and language servers) and the background jobs. Each
// piece bounds its own wait, and calling it twice runs nothing twice.
func (r *root) close() {
	closers := r.closers
	r.closers = nil
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}

// newRoot builds the agent for this session and the stores around it. It
// returns the root even when it fails, so the caller can close whatever
// was already started.
func newRoot(fs *flag.FlagSet, o *options, cfg *config.Config, prov llm.Provider, needsConnect bool, cwd, sessDir, initial string, format headless.Format, sess *session.Session, history []llm.Message) (*root, error) {
	r := &root{opts: o, fs: fs, cfg: cfg, prov: prov, needsConnect: needsConnect, cwd: cwd, sessDir: sessDir, initial: initial, format: format, sess: sess, history: history}
	cfgPath, _ := config.Path()
	cfgDir := filepath.Dir(cfgPath)
	r.cfgPath, r.cfgDir = cfgPath, cfgDir
	jobs := &tool.Jobs{}
	r.onClose(jobs.KillAll)
	mem := memory.Open(filepath.Dir(sessDir), cwd)
	mem.Confirm.Store(cfg.MemoryConfirm)
	if em, ok := prov.(llm.Embedder); ok && cfg.EmbeddingModel != "" {
		model := cfg.EmbeddingModel
		mem.Embed = func(ctx context.Context, texts []string) ([][]float32, error) { return em.Embed(ctx, model, texts) }
	}
	pool := newProviderPool(cfg, prov)
	promptOv, err := prompt.LoadOverride(os.Getenv, o.appendSystem)
	if err != nil {
		return r, err
	}
	typed := &weburl.Set{}
	typed.Note(initial)
	typed.Note(o.promptText)
	filter, err := parseToolFilter(o.coreTools, o.excludeTools)
	if err != nil {
		return r, err
	}
	promptOv.NoProject = o.safeMode
	unattended := o.promptText != "" || o.inFormat == "stream-json"
	if unattended && promptOv.Base == "" && !envOff()["leanprompt"] {
		// A run with nobody at the terminal starts from the headless base
		// (see prompt.Headless); JANETCODE_SYSTEM_MD still replaces it.
		// The slim edit tools do not advertise then_run (see
		// internal/tool/slim.go), so the prompt does not name it.
		off := envOff()
		promptOv.Base = prompt.HeadlessFor(!off["headlesscore"] && !off["slimtools"], !off["literal"], !off["rulesfirst"])
	}
	// A mode the managed policy disables is a usage error when asked for
	// on the command line: quietly running with it off would be a run
	// the caller did not ask for either.
	for _, m := range []struct {
		on         bool
		flag, mode string
	}{{o.autoMode, "auto", managed.ModeAuto}, {o.autoEdits, "auto-edits", managed.ModeEdits}} {
		if m.on && cfg.Managed.ModeDisabled(m.mode) {
			return r, headless.BadInput("--%s: %v", m.flag, cfg.Managed.Refusal("--"+m.flag))
		}
	}
	gate, err := newTrustGate(cfgDir, o.trustLevel, os.Getenv)
	if err != nil {
		return r, err
	}
	r.gate = gate
	deps := agentDeps{safe: o.safeMode, tools: filter, headless: unattended, gate: gate, prov: prov, cfg: cfg, cwd: cwd, cfgDir: cfgDir, sessDir: sessDir, mem: mem, jobs: jobs, version: version, readOnly: o.readonly, autoEdits: o.autoEdits, autoMode: o.autoMode, sandboxOn: o.sandboxOn, models: pool.For, promptOverride: promptOv, typed: typed, running: &tool.Running{}, extraDirs: o.addDir, fileMode: o.fileMode, admit: o.admit, attach: o.attach, symbols: treesitter.Parser()}
	built, err := buildAgent(deps, sess, history)
	if err != nil {
		return r, err
	}
	r.onClose(built.close)
	ag := built.agent
	if err := filter.check(ag.Tools); err != nil {
		return r, err
	}
	filter.activate(ag.Tools)
	if o.dryRun {
		ag.SetDryRun(true)
	}

	r.servers = mcptool.Manager{Options: mcp.Options{Root: cwd, Version: version, OnElicit: mcptool.ElicitViaAsker(func() agent.Asker { return r.elicitAsker(ag) })}, Registry: ag.Tools}
	if pol := cfg.Managed; pol != nil {
		r.servers.Allowed = func(name string) error {
			if !pol.MCPAllowed(name) {
				return pol.Refusal(fmt.Sprintf("MCP server %q", name))
			}
			return nil
		}
	}
	r.servers.Policy = extension.Policy{Store: extension.Store{Path: extension.StorePath(cfgDir)}, Cwd: cwd}
	r.onClose(r.servers.Close)
	projectServers, projectHash, err := mcp.LoadProject(cwd)
	if err != nil {
		return r, err
	}
	ts := trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}
	if o.safeMode {
		ts.Path = "" // nothing of the project's is trusted, whatever was approved before
	}
	tokens := &mcp.TokenStore{Path: filepath.Join(cfgDir, "mcp-auth.json")}
	r.servers.Options.Tokens = tokens
	r.jobs, r.mem, r.pool, r.typed = jobs, mem, pool, typed
	r.deps, r.built, r.ag = deps, built, ag
	r.diag, r.archive, r.skills = built.diag, built.archive, built.skills
	r.trust, r.tokens = ts, tokens
	r.projectServers, r.projectHash = projectServers, projectHash
	return r, nil
}

// newTrustGate builds the project trust gate over the same store the
// per-file approvals live in, with --trust (or JANETCODE_TRUST) as this
// run's level when given.
func newTrustGate(cfgDir, flagLevel string, getenv func(string) string) (*trust.Gate, error) {
	g := &trust.Gate{Store: trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}}
	for _, src := range []struct{ name, v string }{{"--trust", flagLevel}, {"JANETCODE_TRUST", getenv("JANETCODE_TRUST")}} {
		if strings.TrimSpace(src.v) == "" {
			continue
		}
		lvl, err := trust.ParseLevel(src.v)
		if err != nil {
			return nil, headless.BadInput("%s: %v", src.name, err)
		}
		g.Flag = lvl
		break
	}
	return g, nil
}

// wireProject folds the project's own hooks, verification and permission
// files into the agent -- each only as far as it is trusted -- and
// registers the inspect subjects and the feature catalogue.
func (r *root) wireProject() error {
	o, cfg, ag, cwd, cfgDir, cfgPath, ts := r.opts, r.cfg, r.ag, r.cwd, r.cfgDir, r.cfgPath, r.trust
	sess, mem, jobs, diag, repo, task := r.sess, r.mem, r.jobs, r.diag, r.built.repo, r.built.task
	archive, skills, built, fs := r.archive, r.skills, r.built, r.fs
	servers := &r.servers
	projectServers, projectHash := r.projectServers, r.projectHash
	hooksCfg, hooksHash, err := hook.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project hooks ignored)\n", err)
	}
	hooksTrusted := hooksCfg.Len() > 0 && r.gate.Exec(cwd, ts.Trusted(trust.HooksKey(cwd), hooksHash))
	ag.Hooks = &hook.Runner{Config: cfg.Hooks, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
	if hooksTrusted {
		var hookNotes []string
		ag.Hooks.Config, hookNotes = hook.MergeScoped(cfg.Hooks, hooksCfg)
		for _, n := range hookNotes {
			fmt.Fprintf(os.Stderr, "[hooks] %s\n", n)
		}
	} else if hooksCfg.Len() > 0 && o.headless() && !o.safeMode {
		fmt.Fprintf(os.Stderr, "[hook] %s is not trusted yet; open the TUI and run /hooks trust\n", hook.ProjectFile)
	}
	r.loadEnvrc(cwd)
	// session_end runs before the servers and jobs the hooks might look
	// at are closed (closers run newest first).
	r.onClose(func() { ag.EndSession("exit") })
	verifyCfg, verifyHash, err := verify.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project verification ignored)\n", err)
	}
	verifyDefined := len(verifyCfg.Commands) > 0 || verifyCfg.Mutate != ""
	verifyTrusted := verifyDefined && r.gate.Exec(cwd, ts.Trusted(trust.VerifyKey(cwd), verifyHash))
	if verifyTrusted {
		ag.Verify = &verify.Runner{Config: verifyCfg, Cwd: cwd, Mutate: mutate.ForVerify, Log: func(s string) { fmt.Fprintf(os.Stderr, "[verify] %s\n", s) }}
	} else if verifyDefined {
		ag.VerifyUntrusted = true
		if o.headless() && !o.safeMode {
			fmt.Fprintf(os.Stderr, "[verify] %s is not trusted yet; open the TUI and run /verify trust\n", verify.ProjectFile)
		}
	}
	rules, projRules, permHash, permTrusted, err := r.permissionRules(cwd)
	if err != nil {
		return err
	}
	if !permTrusted && len(projRules.Allow) > 0 && o.headless() && !o.safeMode {
		fmt.Fprintf(os.Stderr, "[permissions] %s has allow rules that are not trusted yet; open the TUI and run /permissions trust\n", perm.ProjectFile)
	}
	ag.Rules = rules
	ag.SaveRule = func(r perm.Rule) error { return config.AddPermission(cfgPath, "allow", r) }
	// The ledger is keyed like the hint store below: per project, in the
	// config directory, never in the project itself.
	ag.Ledger = perm.OpenLedger(cfgDir, memory.Key(cwd))
	// Paths a write always asks about, whatever the mode: the defaults
	// plus whatever protected_paths adds to them.
	ag.Protected = perm.NewProtected(cfg.ProtectedPaths)
	// Validated by config.Load, so the error cannot happen here.
	ag.EgressPolicy, _ = cfg.EgressPolicy()
	// With a policy, a sandboxed bash command reaches the network only
	// through the filtering proxy (see Agent.StartEgressProxy).
	if px, err := ag.StartEgressProxy(); err != nil {
		return fmt.Errorf("egress policy: %w", err)
	} else if px != nil {
		r.onClose(func() { _ = px.Close() })
	}
	projectTrusted := len(projectServers) > 0 && r.gate.Exec(cwd, ts.Trusted(cwd, projectHash))

	debugPath, debugFrom := o.debugFile, "--debug"
	if debugPath == "" {
		debugPath, debugFrom = os.Getenv("JANETCODE_DEBUG"), "JANETCODE_DEBUG"
	}
	if path := debugPath; path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("%s: %w", debugFrom, err)
		}
		r.onClose(func() { _ = f.Close() })
		ag.Debug = f
		servers.Options.Debug = f
	}

	subjects := inspect.New()
	registerSubjects(subjects, subjectDeps{
		agent: ag, session: sess, cfg: cfg, cfgPath: cfgPath, cwd: cwd, version: version,
		archive: archive, mem: mem, repo: repo, diag: diag, mcp: servers, jobs: jobs,
		task: task, skills: skills, rules: built.rules, ruleInjector: built.ruleInjector,
	})
	if (r.opts.promptText != "" || r.opts.inFormat == "stream-json") && !envOff()["headlesscore"] {
		// After the build, so stepHeadlessCore did not see it.
		ag.Tools.AddDeferred(inspect.Tool{Registry: subjects})
	} else {
		ag.Tools.Add(inspect.Tool{Registry: subjects})
	}
	// The feature catalogue is built from the tables this run is already
	// holding: the flag set, the tool registry, the command table and the
	// Config struct. It costs a few hundred small structs and no I/O.
	catalog := buildCatalog(fs, ag.Tools)
	registerFeatureSubject(subjects, catalog)
	registerThemeSubject(subjects, cfg)
	registerTrustSubject(subjects, r.gate, r.built.cd.Dir)
	registerExtensionsSubject(subjects, extensionEnv{cfg: cfg, cfgPath: cfgPath, cfgDir: r.cfgDir, cwd: r.built.cd.Dir,
		gate: r.gate, ts: ts, safe: o.safeMode, mgr: servers})
	registerEgressSubject(subjects, ag)
	// Deferred: the model gets the schema only after asking for it, so a
	// session that never wonders about the harness never pays for it.
	ag.Tools.AddDeferred(feature.Introspect{Catalog: catalog})
	r.hooksCfg, r.hooksHash, r.hooksTrusted = hooksCfg, hooksHash, hooksTrusted
	r.verifyCfg, r.verifyHash = verifyCfg, verifyHash
	r.verifyDefined, r.verifyTrusted = verifyDefined, verifyTrusted
	r.projRules, r.permHash, r.permTrusted = projRules, permHash, permTrusted
	r.projectTrusted = projectTrusted
	r.subjects, r.catalog = subjects, catalog
	r.registerProjectRooted(r.built.cd)
	return nil
}

// permissionRules assembles the permission rules for a project at cwd:
// the configuration's, then the project's own file as far as it is
// trusted (deny and ask rules need no trust: they only ever take
// something away), then this run's --allow/--deny/--ask, and last the
// managed policy's, which may drop every allow rule not its own.
// /cd runs it again for the directory it moved to.
func (r *root) permissionRules(cwd string) (rules, projRules perm.Rules, hash string, trusted bool, err error) {
	rules, _ = r.cfg.Permissions.Rules() // validated by config.Load
	projRules, hash, err = perm.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project permissions ignored)\n", err)
		err = nil
	}
	trusted = len(projRules.Allow) > 0 && r.gate.Exec(cwd, r.trust.Trusted(trust.PermissionsKey(cwd), hash))
	if trusted {
		rules = perm.Merge(rules, projRules)
	} else {
		rules = perm.Merge(rules, perm.Rules{Deny: projRules.Deny, Ask: projRules.Ask}) // deny and ask rules need no trust
	}
	for _, spec := range []struct {
		name, flag string
		v          perm.Verdict
	}{{"allow", r.opts.allow, perm.Allowed}, {"deny", r.opts.deny, perm.Denied}, {"ask", r.opts.ask, perm.Ask}} {
		for _, part := range perm.SplitList(spec.flag) {
			pr, perr := perm.Parse(part)
			if perr != nil {
				// The rule came from the command line: a usage error.
				return rules, projRules, hash, trusted, headless.BadInput("--%s: %v", spec.name, perr)
			}
			if spec.v == perm.Ask {
				rules.AddAsk(pr)
				continue
			}
			rules.Add(spec.v, pr)
		}
	}
	rules = r.cfg.Managed.Apply(rules)
	return rules, projRules, hash, trusted, nil
}
