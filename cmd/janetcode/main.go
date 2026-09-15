// Command janetcode is an interactive terminal coding agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/acp"
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rates"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	_ "github.com/xmonader/janetcode/internal/treesitter" // registers the grammar parser when built with -tags treesitter
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/verify"
	"github.com/xmonader/janetcode/internal/webui"
	"github.com/xmonader/janetcode/internal/weburl"
	"github.com/xmonader/janetcode/internal/worktree"
)

// version is set at build time via -ldflags "-X main.version=...". A
// plain `go install .../cmd/janetcode@v0.2.0` has no ldflags, so the
// module version or the commit the Go toolchain recorded stands in.
var version = "dev"

// pseudoVersionRe matches a Go pseudo-version: a timestamp and a commit
// where a tag would be.
var pseudoVersionRe = regexp.MustCompile(`\.\d{14}-[0-9a-f]{12}`)

func init() {
	if version != "dev" {
		return
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// A tagged module version is the release; a pseudo-version
	// (v0.2.1-0.20260910193945-02799f51583f) is a local build and the
	// commit it names says more, so it falls through to the VCS fields.
	if v := bi.Main.Version; v != "" && v != "(devel)" && !pseudoVersionRe.MatchString(v) {
		version = v
		return
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		version = "dev-" + rev + dirty
	}
}

func main() {
	err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "janetcode:", err)
		if keyRejected(err) {
			fmt.Fprintln(os.Stderr, "janetcode: the endpoint rejected the API key; check the key for the provider in use (janetcode --doctor shows which key each provider has)")
		}
	}
	// The status says which of the handful of things that can go wrong
	// went wrong, so a script can tell a missing key from a turn limit
	// without reading the message.
	os.Exit(headless.ExitCode(err))
}

// keyRejected reports whether err is a provider's 401 or 403: the key was
// sent and refused, as opposed to missing.
func keyRejected(err error) bool {
	status := 0
	if oe := (*openai.APIError)(nil); errors.As(err, &oe) {
		status = oe.Status
	} else if ae := (*anthropic.APIError)(nil); errors.As(err, &ae) {
		status = ae.Status
	}
	return status == 401 || status == 403
}

// printFlags is flag.PrintDefaults for the flags keep selects, so the
// help can show the everyday flags apart from the eval ones.
func printFlags(fs *flag.FlagSet, keep func(*flag.Flag) bool) {
	fs.VisitAll(func(f *flag.Flag) {
		if !keep(f) {
			return
		}
		name, usage := flag.UnquoteUsage(f)
		line := "  -" + f.Name
		if name != "" {
			line += " " + name
		}
		line += "\n    \t" + strings.ReplaceAll(usage, "\n", "\n    \t")
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			if name == "string" {
				line += fmt.Sprintf(" (default %q)", f.DefValue)
			} else {
				line += fmt.Sprintf(" (default %s)", f.DefValue)
			}
		}
		fmt.Fprintln(fs.Output(), line)
	})
}

func run(args []string) error {
	fs, o := newFlagSet()
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.continueC {
		o.cont = true
	}
	if o.resumeR != "" {
		o.resume = o.resumeR
	}
	if done, err := earlyMode(fs, o); done {
		return err
	}
	initial := strings.TrimSpace(strings.Join(fs.Args(), " "))
	format, err := headless.ParseFormat(o.outFormat)
	if err != nil {
		return fmt.Errorf("%w: %v", headless.ErrBadInput, err)
	}
	if err := checkModes(o, initial, format); err != nil {
		return err
	}
	if done, err := offlineMode(o); done {
		return err
	}
	cfg, cerr := config.Load(config.Overrides{Provider: o.provider, Model: o.model, Effort: o.effort, Theme: o.themeFlag, AutoApprove: o.yolo})
	err = cerr
	// Interactive first run: open the TUI and ask for a provider instead
	// of failing. Headless runs still need working credentials.
	needsConnect := err != nil && errors.Is(err, config.ErrNoCredentials) && o.promptText == "" && term.IsTerminal(int(os.Stdin.Fd()))
	// Inspecting the configuration is what you reach for when the
	// configuration is the problem, so it must not need a working one.
	// The subjects that describe a running session are unavailable, and
	// the listing says which those are.
	if o.inspectWhat != "" && err != nil && errors.Is(err, config.ErrNoCredentials) {
		r := inspect.New()
		cfgPath, _ := config.Path()
		cwd, _ := os.Getwd()
		registerOfflineSubjects(r, cfg, cfgPath, cwd, err)
		registerFeatureSubject(r, buildCatalog(fs, nil))
		registerThemeSubject(r, cfg)
		return inspectOnce(r, o.inspectWhat, os.Stdout)
	}
	if err == nil {
		// Prices for models the file does not list come from LiteLLM's
		// public table, cached a day under the config directory. The
		// cache is read now; a refresh runs in the background.
		if cfgPath, perr := config.Path(); perr == nil {
			dir := filepath.Dir(cfgPath)
			cfg.RatesTable = rates.LoadLazy(dir)
			if !rates.Fresh(dir) {
				// The refresh writes model-rates.json into the config
				// directory, and it is background work only for as long as
				// the run lasts: a goroutine still downloading 2.3 MB after
				// run returns outlives whatever set that directory up. A
				// test's TempDir cleanup raced exactly that and failed with
				// ".config/janetcode not empty". Cancel it and wait for it
				// on the way out, so nothing writes there after the run.
				rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = rates.Refresh(rctx, dir)
				}()
				defer func() {
					cancel()
					<-done
				}()
			}
		}
	}
	if err == nil && o.promptText != "" {
		for _, n := range cfg.Notes {
			fmt.Fprintln(os.Stderr, "janetcode:", n)
		}
	}
	if err != nil && !needsConnect {
		// A config file that cannot be used is a different thing from
		// credentials that are merely missing, and exits differently.
		// config.Load says which it is in the error's type, so nothing
		// here has to guess from the wording.
		return err
	}
	var prov llm.Provider
	if !needsConnect {
		if prov, err = buildProvider(cfg); err != nil {
			return err
		}
	}
	// The turn cap and the compaction threshold are settings, not eval
	// plumbing: a headless run driven by a benchmark needs the same two
	// knobs the eval runner takes, and applying them to the config means
	// every path downstream -- headless, TUI and ACP -- reads one value.
	if o.maxTurns > 0 {
		cfg.MaxIterations = o.maxTurns
	}
	if o.compactAt > 0 {
		cfg.CompactAt = o.compactAt
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if o.wtBranch != "" {
		wt, err := worktree.Open(context.Background(), cwd, o.wtBranch)
		if err != nil {
			return err
		}
		verb := "reusing"
		if wt.Created {
			verb = "created"
		}
		fmt.Fprintf(os.Stderr, "worktree: %s %s on branch %s\n", verb, wt.Path, wt.Branch)
		defer func() {
			fmt.Fprintf(os.Stderr, "worktree: branch %s has %s (%s)\nmerge it with: git merge %s\n", wt.Branch, wt.Summarize(context.Background()), wt.Path, wt.Branch)
		}()
		// Everything downstream is rooted at cwd: tools, the repository
		// map, language servers, hooks and the session's own record.
		if err := os.Chdir(wt.Path); err != nil {
			return err
		}
		cwd = wt.Path
	}
	if o.whatIf != "" {
		if prov == nil {
			return errors.New("--what-if needs a configured provider for the live part")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runWhatIf(ctx, cfg, prov, whatIfOptions{
			session: o.whatIf, fromTurn: o.whatIfFrom, off: o.whatIfOff, deny: o.deny, prompt: o.whatIfPrompt,
			keep: o.whatIfKeep, json: o.jsonOut,
		})
	}
	// Re-reading saved results needs no provider and no money: the data is
	// already on disk, and a comparison that was printed once should be
	// printable again when the statistics behind it change.
	if o.evalPath != "" {
		if prov == nil {
			return errors.New("--eval needs a configured provider")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ov, err := prompt.LoadOverride(os.Getenv, o.appendSystem)
		if err != nil {
			return err
		}
		return runEval(ctx, cfg, prov, evalOptions{path: o.evalPath, jobs: o.evalJobs, only: o.evalOnly, jsonOut: o.evalJSON, keep: o.evalKeep,
			runs: o.evalRuns, compare: o.evalCompare, off: o.evalOff, route: o.evalRoute, label: o.evalLabel, prompt: ov,
			timeout: o.evalTimeout, maxTurns: o.evalTurns, noToolLimit: o.evalNoLimit, compactAt: o.evalCompact, facts: o.evalFacts, factsBlind: o.evalBlind})
	}

	sessDir, err := session.Dir()
	if err != nil {
		return err
	}
	var (
		sess     *session.Session
		history  []llm.Message
		resumeID = o.resume
	)
	if o.cont && resumeID == "" {
		if resumeID, err = session.Latest(sessDir, cwd); err != nil {
			return err
		}
	}
	if resumeID != "" {
		if sess, history, err = session.Open(sessDir, resumeID); err != nil {
			return fmt.Errorf("resume: %w", err)
		}
	} else {
		if sess, err = session.New(sessDir, cwd, cfg.Provider, cfg.Model); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}
	if o.bg {
		prompt := strings.TrimSpace(o.promptText)
		if prompt == "" {
			prompt = initial
		}
		if prompt == "" {
			return fmt.Errorf("%w: --bg needs a prompt (-p or an argument)", headless.ErrBadInput)
		}
		return launchBackground(sess, prompt, o.addDir)
	}

	cfgPath, _ := config.Path()
	cfgDir := filepath.Dir(cfgPath)
	jobs := &tool.Jobs{}
	defer jobs.KillAll()
	mem := memory.Open(filepath.Dir(sessDir), cwd)
	if em, ok := prov.(llm.Embedder); ok && cfg.EmbeddingModel != "" {
		model := cfg.EmbeddingModel
		mem.Embed = func(ctx context.Context, texts []string) ([][]float32, error) { return em.Embed(ctx, model, texts) }
	}
	pool := newProviderPool(cfg, prov)
	promptOv, err := prompt.LoadOverride(os.Getenv, o.appendSystem)
	if err != nil {
		return err
	}
	typed := &weburl.Set{}
	typed.Note(initial)
	typed.Note(o.promptText)
	deps := agentDeps{prov: prov, cfg: cfg, cwd: cwd, cfgDir: cfgDir, sessDir: sessDir, mem: mem, jobs: jobs, version: version, readOnly: o.readonly, autoEdits: o.autoEdits, autoMode: o.autoMode, sandboxOn: o.sandboxOn, models: pool.For, promptOverride: promptOv, typed: typed, running: &tool.Running{}, extraDirs: o.addDir}
	built, err := buildAgent(deps, sess, history)
	if err != nil {
		return err
	}
	defer built.close()
	ag, task, diag, repo := built.agent, built.task, built.diag, built.repo
	archive, skills := built.archive, built.skills
	_ = task
	_ = repo

	servers := mcptool.Manager{Options: mcp.Options{Root: cwd, Version: version}, Registry: ag.Tools}
	defer servers.Close()
	projectServers, projectHash, err := mcp.LoadProject(cwd)
	if err != nil {
		return err
	}
	ts := trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}
	tokens := &mcp.TokenStore{Path: filepath.Join(cfgDir, "mcp-auth.json")}
	servers.Options.Tokens = tokens
	if o.mcpAuth != "" {
		return authorizeMCP(o.mcpAuth, cfg.MCPServers, projectServers, tokens)
	}
	hooksCfg, hooksHash, err := hook.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project hooks ignored)\n", err)
	}
	hooksTrusted := hooksCfg.Len() > 0 && ts.Trusted(trust.HooksKey(cwd), hooksHash)
	ag.Hooks = &hook.Runner{Config: cfg.Hooks, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
	if hooksTrusted {
		var hookNotes []string
		ag.Hooks.Config, hookNotes = hook.MergeScoped(cfg.Hooks, hooksCfg)
		for _, n := range hookNotes {
			fmt.Fprintf(os.Stderr, "[hooks] %s\n", n)
		}
	} else if hooksCfg.Len() > 0 && o.promptText != "" {
		fmt.Fprintf(os.Stderr, "[hook] %s is not trusted yet; open the TUI and run /hooks trust\n", hook.ProjectFile)
	}
	verifyCfg, verifyHash, err := verify.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project verification ignored)\n", err)
	}
	verifyDefined := len(verifyCfg.Commands) > 0 || verifyCfg.Mutate != ""
	verifyTrusted := verifyDefined && ts.Trusted(trust.VerifyKey(cwd), verifyHash)
	if verifyTrusted {
		ag.Verify = &verify.Runner{Config: verifyCfg, Cwd: cwd, Mutate: mutate.ForVerify, Log: func(s string) { fmt.Fprintf(os.Stderr, "[verify] %s\n", s) }}
	} else if verifyDefined {
		ag.VerifyUntrusted = true
		if o.promptText != "" {
			fmt.Fprintf(os.Stderr, "[verify] %s is not trusted yet; open the TUI and run /verify trust\n", verify.ProjectFile)
		}
	}
	rules, _ := cfg.Permissions.Rules() // validated by config.Load
	projRules, permHash, err := perm.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project permissions ignored)\n", err)
	}
	permTrusted := len(projRules.Allow) > 0 && ts.Trusted(trust.PermissionsKey(cwd), permHash)
	if permTrusted {
		rules = perm.Merge(rules, projRules)
	} else {
		rules = perm.Merge(rules, perm.Rules{Deny: projRules.Deny}) // deny rules need no trust
		if len(projRules.Allow) > 0 && o.promptText != "" {
			fmt.Fprintf(os.Stderr, "[permissions] %s has allow rules that are not trusted yet; open the TUI and run /permissions trust\n", perm.ProjectFile)
		}
	}
	for _, spec := range []struct {
		flag string
		v    perm.Verdict
	}{{o.allow, perm.Allowed}, {o.deny, perm.Denied}} {
		for _, part := range strings.Split(spec.flag, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			r, err := perm.Parse(part)
			if err != nil {
				return err
			}
			rules.Add(spec.v, r)
		}
	}
	ag.Rules = rules
	ag.SaveRule = func(r perm.Rule) error { return config.AddPermission(cfgPath, "allow", r) }
	// The ledger is keyed like the hint store below: per project, in the
	// config directory, never in the project itself.
	ag.Ledger = perm.OpenLedger(cfgDir, memory.Key(cwd))
	projectTrusted := len(projectServers) > 0 && ts.Trusted(cwd, projectHash)

	if path := os.Getenv("JANETCODE_DEBUG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("JANETCODE_DEBUG: %w", err)
		}
		defer f.Close()
		ag.Debug = f
		servers.Options.Debug = f
	}

	subjects := inspect.New()
	registerSubjects(subjects, subjectDeps{
		agent: ag, session: sess, cfg: cfg, cfgPath: cfgPath, cwd: cwd, version: version,
		archive: archive, mem: mem, repo: repo, diag: diag, mcp: &servers, jobs: jobs,
		task: task, skills: skills, rules: built.rules, ruleInjector: built.ruleInjector,
	})
	ag.Tools.Add(inspect.Tool{Registry: subjects})
	// The feature catalogue is built from the tables this run is already
	// holding: the flag set, the tool registry, the command table and the
	// Config struct. It costs a few hundred small structs and no I/O.
	catalog := buildCatalog(fs, ag.Tools)
	registerFeatureSubject(subjects, catalog)
	registerThemeSubject(subjects, cfg)
	// Deferred: the model gets the schema only after asking for it, so a
	// session that never wonders about the harness never pays for it.
	ag.Tools.AddDeferred(feature.Introspect{Catalog: catalog})
	if o.inspectWhat != "" {
		return inspectOnce(subjects, o.inspectWhat, os.Stdout)
	}

	if o.recipeName != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if sess != nil {
			defer func() { _ = sess.Sync() }()
		}
		r, err := recipe.Load(cwd, o.recipeName)
		if err != nil {
			return fmt.Errorf("--recipe: %w", err)
		}
		// A recipe is built through the same agent the normal headless
		// run (-p) uses, so its steps see the same tools — including a
		// configured MCP server's, when the recipe actually calls one.
		// Starting every server for a recipe that never touches one would
		// slow down the common case (plain file and shell steps) for
		// nothing, so this only starts them when a step's tool name says
		// otherwise; servers.Close is already deferred above, so whatever
		// gets started here still shuts down on exit either way.
		if agent.NeedsMCP(r) {
			toStart := map[string]mcp.ServerConfig{}
			for n, c := range cfg.MCPServers {
				toStart[n] = c
			}
			if projectTrusted {
				for n, c := range projectServers {
					toStart[n] = c
				}
			} else if len(projectServers) > 0 {
				fmt.Fprintf(os.Stderr, "[mcp] %s lists servers that are not trusted yet; open the TUI and run /mcp trust\n", mcp.ProjectFile)
			}
			servers.StartAll(ctx, toStart, func(name string, c *mcp.Client, err error) {
				if err != nil {
					fmt.Fprintf(os.Stderr, "[mcp] %s failed: %v\n", name, err)
					return
				}
				fmt.Fprintf(os.Stderr, "[mcp] %s ready: %d tools\n", name, len(c.Tools()))
			})
		}
		return runRecipeFlag(ctx, ag, cwd, r, fs.Args(), os.Stdout, os.Stderr, os.Stdin)
	}

	// The hint engine remembers what it has already said, per project,
	// in the config directory — never in the project itself. The record
	// is read once, here; it is written only when a hint fires.
	hintStore := hints.Open(cfgDir, memory.Key(cwd))
	hintEngine := hints.New(hintStore.Load())
	hintEngine.Off = !cfg.ShowHints()
	hintEngine.Record = func(string) { _ = hintStore.Save(hintEngine.Shown(), hintEngine.Skipped()) }

	// Which features this user has reached for, counted in memory and
	// written once when the session ends. It stays on this machine:
	// there is no endpoint and nothing in janetcode reads it but the
	// hint rules and /stats.
	usage := feature.OpenUsage(cfgDir)
	defer func() { _ = usage.Flush() }()
	fs.Visit(func(f *flag.Flag) { usage.Note(f.Name) })

	if o.acpMode {
		// The editor owns stdout: only ACP frames may go there, so every
		// other message in this mode is already on stderr.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		first := built
		used := false
		srv := acp.New(acp.Options{
			In: os.Stdin, Out: os.Stdout, Log: os.Stderr, Version: version,
			NewAgent: func(sessionCwd, resume string) (acp.TurnRunner, *session.Session, error) {
				// The editor says which directory the session is for, and
				// it is not necessarily the one janetcode was launched
				// from — an editor opens a workspace, not a terminal. Every
				// tool is rooted at that directory, so it has to be the one
				// the agent is built for.
				root := cwd
				if sessionCwd != "" {
					if abs, err := filepath.Abs(sessionCwd); err == nil {
						root = abs
					}
				}
				s, hist := sess, history
				if resume != "" {
					var err error
					if s, hist, err = session.Open(sessDir, resume); err != nil {
						return nil, nil, err
					}
					if s.Meta.Cwd != "" {
						root = s.Meta.Cwd
					}
				} else if used || root != cwd {
					var err error
					if s, err = session.New(sessDir, root, cfg.Provider, cfg.Model); err != nil {
						return nil, nil, err
					}
					hist = nil
				}
				// The agent this process already built is only right when
				// the session is for the directory it was built for.
				if !used && resume == "" && root == cwd {
					used = true
					return acp.Wrap(first.agent), s, nil
				}
				used = true
				d := deps
				d.cwd = root
				b, err := buildAgent(d, s, hist)
				if err != nil {
					return nil, nil, err
				}
				return acp.Wrap(b.agent), s, nil
			},
		})
		fmt.Fprintf(os.Stderr, "janetcode %s serving ACP on stdio (%s/%s in %s)\n", version, cfg.Provider, cfg.Model, cwd)
		return srv.Serve(ctx)
	}

	if o.promptText != "" || o.watchCmd != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		text, atts := prompt.ExpandMentions(cwd, o.promptText)
		for _, a := range atts {
			fmt.Fprintf(os.Stderr, "[attached] %s\n", a.Path)
		}
		if extra := pipedStdin(); extra != "" {
			text += "\n\n<stdin>\n" + extra + "\n</stdin>"
		}
		toStart := map[string]mcp.ServerConfig{}
		for n, c := range cfg.MCPServers {
			toStart[n] = c
		}
		if projectTrusted {
			for n, c := range projectServers {
				toStart[n] = c
			}
		} else if len(projectServers) > 0 {
			fmt.Fprintf(os.Stderr, "[mcp] %s lists servers that are not trusted yet; open the TUI and run /mcp trust\n", mcp.ProjectFile)
		}
		servers.StartAll(ctx, toStart, func(name string, c *mcp.Client, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "[mcp] %s failed: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "[mcp] %s ready: %d tools\n", name, len(c.Tools()))
		})
		// Appending syncs on a timer, not per event; a headless run has no
		// settling step to force the last of it out, so it does that here.
		if sess != nil {
			defer func() { _ = sess.Sync() }()
		}
		return headless.Run(ctx, headless.Options{
			Agent: ag, Session: sess, Prompt: text, Images: prompt.ImageBlocks(atts),
			Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
			Until: o.until, Watch: o.watchCmd, Cwd: cwd, Memory: mem, AutoMemory: cfg.AutoMemory(),
			Format: format, MaxCostUSD: o.maxCost,
		})
	}
	if o.web {
		if needsConnect {
			return fmt.Errorf("no credentials yet: run janetcode without --web and use /connect, or set a key")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		toStart := map[string]mcp.ServerConfig{}
		for n, c := range cfg.MCPServers {
			toStart[n] = c
		}
		if projectTrusted {
			for n, c := range projectServers {
				toStart[n] = c
			}
		}
		servers.StartAll(ctx, toStart, func(name string, c *mcp.Client, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "[mcp] %s failed: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "[mcp] %s ready: %d tools\n", name, len(c.Tools()))
		})
		if sess != nil {
			defer func() { _ = sess.Sync() }()
		}
		return webui.Run(ctx, webui.Options{
			Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd,
			Version: version, Addr: o.webAddr,
			Inspect: subjects, Memory: mem, Jobs: jobs, Features: catalog, ConfigDir: cfgDir,
			Typed: typed, Config: cfg, ConfigPath: cfgPath, Models: pool.For,
			Workspace: built.ws,
			Doctor: func(w io.Writer) error {
				return doctor(w, config.Overrides{Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Theme: cfg.Theme})
			},
		})
	}
	return tui.Run(tui.Options{
		Notes: cfg.Notes,
		Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd, ConfigDir: cfgDir,
		Workspace: built.ws,
		Memory:    mem, AutoMemory: cfg.AutoMemory(),
		Config: cfg, ConfigPath: cfgPath, Build: buildProvider, Connect: needsConnect,
		Resumed: len(history), Version: version, Theme: cfg.Theme, Themes: cfg.ThemeSet,
		InitialPrompt: initial,
		MCP: &tui.MCPOptions{
			Manager: &servers, Global: cfg.MCPServers,
			Project: projectServers, ProjectHash: projectHash, ProjectTrusted: projectTrusted, Trust: ts,
			Tokens: tokens,
		},
		Hooks:    &tui.HookOptions{Global: cfg.Hooks, Project: hooksCfg, ProjectHash: hooksHash, ProjectTrusted: hooksTrusted, Trust: ts},
		LSP:      diag,
		Inspect:  subjects,
		Features: catalog,
		Hints:    hintEngine,
		Usage:    usage,
		Untrusted: untrustedProjectFiles(untrustedInput{
			mcp:       len(projectServers) > 0 && !projectTrusted,
			hooks:     hooksCfg.Len() > 0 && !hooksTrusted,
			perms:     len(projRules.Allow) > 0 && !permTrusted,
			verifying: verifyDefined && !verifyTrusted,
		}),
		UnpricedModel: unpricedModel(cfg, os.Getenv),
		Rules:         built.ruleInjector,
		Models:        pool.For,
		Jobs:          jobs,
		Running:       deps.running,
		Typed:         typed,
		AutoTitle:     true,
		Doctor: func(w io.Writer) error {
			return doctor(w, config.Overrides{Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Theme: cfg.Theme})
		},
		Verify: &tui.VerifyOptions{Config: verifyCfg, ProjectHash: verifyHash, ProjectTrusted: verifyTrusted, Trust: ts},
		Perms: &tui.PermOptions{Project: projRules, ProjectHash: permHash, ProjectTrusted: permTrusted, Trust: ts,
			Save: func(kind string, r perm.Rule) error { return config.AddPermission(cfgPath, kind, r) }},
	})
}

// pipedStdin returns piped input (e.g. `git diff | janetcode -p "review"`),
// or "" when stdin is a terminal or empty. Capped at 1 MB.
// pipedStdin returns text piped or redirected into the process. Only pipes
// and regular files are read: a terminal is interactive, and a socket or
// other device held open by a parent process would block forever.
func pipedStdin() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeNamedPipe == 0 && !fi.Mode().IsRegular()) {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func printSessions() error {
	dir, err := session.Dir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	infos, err := session.List(dir, cwd)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Println("no sessions for", cwd)
		return nil
	}
	for _, in := range infos {
		fork := ""
		if in.Meta.ForkOf != "" {
			fork = " (fork of " + in.Meta.ForkOf + ")"
		}
		fmt.Printf("%s  %s  %-3d msgs  %s/%s  %s%s\n", in.Meta.ID, in.Meta.Created.Local().Format("2006-01-02 15:04"), in.Messages, in.Meta.Provider, in.Meta.Model, in.Label(), fork)
	}
	fmt.Println("\nresume with: janetcode --resume <id>   (or --continue for the newest)")
	return nil
}
