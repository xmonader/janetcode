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
	"sync/atomic"
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
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rates"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	_ "github.com/xmonader/janetcode/internal/treesitter" // registers the grammar parser when built with -tags treesitter
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/verify"
	"github.com/xmonader/janetcode/internal/webui"
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
		if s := err.Error(); strings.Contains(s, "(HTTP 401)") || strings.Contains(s, "(HTTP 403)") {
			fmt.Fprintln(os.Stderr, "janetcode: the endpoint rejected the API key; check the key for the provider in use (janetcode --doctor shows which key each provider has)")
		}
	}
	// The status says which of the handful of things that can go wrong
	// went wrong, so a script can tell a missing key from a turn limit
	// without reading the message.
	os.Exit(headless.ExitCode(err))
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

// declaredFlags holds the flag set of the most recent run, for the
// catalogue drift test. Nothing in the program reads it.
var declaredFlags atomic.Pointer[flag.FlagSet]

func run(args []string) error {
	fs := flag.NewFlagSet("janetcode", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `janetcode %s - a terminal coding agent

Usage: janetcode [flags] [prompt]   interactive session in the current directory,
                                     optionally starting with a prompt
       janetcode -p "prompt" [flags] run one prompt headless and exit

Flags:
`, version)
		isEval := func(name string) bool { return name == "eval" || strings.HasPrefix(name, "eval-") }
		printFlags(fs, func(f *flag.Flag) bool { return !isEval(f.Name) })
		fmt.Fprintf(fs.Output(), "\nEval and benchmark flags (see docs/manual/evals.md):\n")
		printFlags(fs, func(f *flag.Flag) bool { return isEval(f.Name) })
		fmt.Fprintf(fs.Output(), `
Environment: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL
             (a key may be a comma-separated list; they are rotated on rate limits),
             JANETCODE_PROVIDER, JANETCODE_MODEL, JANETCODE_EFFORT, JANETCODE_THEME
             JANETCODE_OFF=<names> disables behaviours for a session (see -eval-off)
             JANETCODE_SYSTEM_MD=<file> replaces the base system prompt
             JANETCODE_DEBUG=<file> appends a timestamped event log
Config file: $XDG_CONFIG_HOME/janetcode/config.json (default ~/.config/janetcode/config.json)
`)
	}
	var (
		promptText   = fs.String("p", "", "run this prompt headless and exit")
		outFormat    = fs.String("output-format", "text", "with -p: how the run reports itself. text prints the answer on stdout and activity on stderr; json prints one result object on stdout (answer, usage, cost and where the figure came from, turns, tool calls, permission denials, verdict, session id, duration); stream-json prints one JSON object per line as things happen, ending with that result")
		maxCost      = fs.Float64("max-cost", 0, "with -p: stop once the run has spent this much in USD (exit 55), checked after every model call. The figure is the provider's when it reports one, otherwise list price")
		maxTurns     = fs.Int("max-turns", 0, "cap the model calls one turn may make (0: the configured max_iterations). The same cap --eval-max-turns applies to an eval run, so a benchmark can give a headless run and an eval run the same budget")
		compactAt    = fs.Int("compact-at", 0, "compact the history at this many estimated tokens instead of the configured value; the same knob --eval-compact-at applies to an eval run")
		until        = fs.String("until", "", "with -p: shell command that must exit 0 before the run is done (re-loops up to 5 times)")
		watchCmd     = fs.String("watch", "", "watch mode: re-run this shell command whenever files change and have the model fix failures (use with --yolo)")
		provider     = fs.String("provider", "", "configured provider or preset name, e.g. anthropic, openai, openrouter, ollama")
		model        = fs.String("model", "", "model id (default claude-opus-5 for anthropic)")
		effort       = fs.String("effort", "", "anthropic effort: low, medium, high, xhigh, max")
		yolo         = fs.Bool("yolo", false, "auto-approve every tool call")
		autoMode     = fs.Bool("auto", false, "auto mode: in-workspace edits and known-safe reads run; other commands ask (/auto toggles it)")
		sandboxOn    = fs.Bool("sandbox", false, "wrap local bash in docker (or sandbox-exec on macOS); file tools stay on the host")
		readonly     = fs.Bool("readonly", false, "read-only: no edits or commands, the model can only inspect")
		continueC    = fs.Bool("c", false, "short for -continue")
		resumeR      = fs.String("r", "", "short for -resume <id>")
		autoEdits    = fs.Bool("auto-edits", false, "auto-accept file edits; commands still ask (/edits toggles it)")
		evalPath     = fs.String("eval", "", "run the eval tasks in this file or directory against the configured provider and exit")
		evalJobs     = fs.Int("eval-jobs", 2, "eval tasks to run at once")
		evalOnly     = fs.String("eval-only", "", "comma-separated task names or tags to run")
		evalJSON     = fs.String("eval-json", "", "write eval results to this JSON file")
		replayPath   = fs.String("replay", "", "re-run a saved session against the current code, with the model's replies read from the recording instead of bought: reports the tool calls it makes now against the ones recorded, and needs no API key")
		evalReport   = fs.String("eval-report", "", "re-read a saved results file and print its report and tool adoption, without running anything; with --eval-compare it prints the paired comparison of two saved files")
		evalKeep     = fs.Bool("eval-keep", false, "keep eval work directories")
		evalRuns     = fs.Int("eval-runs", 1, "repeat every eval task this many times (rates instead of coin flips). One run per task is a coin flip: this set swings 21% between identical runs, so a comparison wants 3 or more")
		evalCompare  = fs.String("eval-compare", "", "results file of a baseline run to compare against (paired by task, bootstrap interval)")
		evalOff      = fs.String("eval-off", "", "comma-separated agent behaviours to disable for an ablation run: coach, syntax, loops, prune, verify, repomap, working, plan, critic")
		evalLabel    = fs.String("eval-label", "", "name recorded in the results file for this variant")
		evalTrain    = fs.String("eval-train", "", "train the trajectory critic from these eval result files (comma-separated files or directories) and exit")
		evalTimeout  = fs.Duration("eval-timeout", 0, "replace every task's timeout with this wall budget (benchmarks give each harness the same)")
		evalTurns    = fs.Int("eval-max-turns", 0, "cap model calls per task run (0: the default of 50)")
		evalNoLimit  = fs.Bool("eval-no-tool-limit", false, "ignore the tasks' max_tool_calls, which other harnesses do not enforce")
		inspectWhat  = fs.String("inspect", "", "print one part of the session state and exit: context, events, archive, retrieval, compaction, tools, config, lsp, mcp, jobs, skills, repomap and more; `list` names them all")
		wtBranch     = fs.String("worktree", "", "run the session in a git worktree on this branch, in a checkout under .git: the real tree is untouched, two sessions can work on one repository at once, and the result is a branch to review and merge")
		acpMode      = fs.Bool("acp", false, "serve the Agent Client Protocol on stdin/stdout, for an ACP editor that launches janetcode as a subprocess (nothing but protocol messages goes to stdout)")
		evalFacts    = fs.Int("eval-facts", 0, "after each run that compacted, probe up to N facts mined from what compaction removed (exit codes, output lines, file lines) and record how many the model still recalls")
		evalBlind    = fs.Bool("eval-facts-blind", false, "probe without expand_output: what the compacted context alone retains")
		evalCompact  = fs.Int("eval-compact-at", 0, "compact the history at this many estimated tokens instead of the default: a low value forces compaction on tasks that would not otherwise reach it, to measure what it costs and what it loses")
		allow        = fs.String("allow", "", "comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'")
		deny         = fs.String("deny", "", "comma-separated permission rules to refuse for this run")
		cont         = fs.Bool("continue", false, "resume the latest session for this directory")
		resume       = fs.String("resume", "", "resume the session with this id")
		showVersion  = fs.Bool("version", false, "print version and exit")
		listSess     = fs.Bool("sessions", false, "list saved sessions for this directory and exit")
		initCfg      = fs.Bool("init", false, "write an example config file and exit")
		mcpAuth      = fs.String("mcp-auth", "", "log in to the named remote MCP server with OAuth (browser flow) and exit")
		appendSystem = fs.String("append-system-prompt", "", "text (or @file) appended to the system prompt; JANETCODE_SYSTEM_MD=<file> replaces the base prompt. Both are recorded in eval results so a prompt A/B is traceable")
		runDoctor    = fs.Bool("doctor", false, "check config, credentials, a live model call, directories, MCP servers, hooks, verification, permissions, skills, memory and language servers, then exit")
		runProof     = fs.Bool("proof", false, "print the proof note (see internal/proof) attached to a commit -- which session, turn and intent wrote each of its files, and what actually demonstrated the work -- then exit; positional argument is a commit (default HEAD), or push [remote] | fetch [remote] to move proof notes to/from a remote, since they are local only until pushed")
		recipeName   = fs.String("recipe", "", "replay a saved recipe (.janetcode/recipes/<name>.json, see /recipe save) as tool calls with no model call and no tokens, then exit; extra positional arguments are k=v parameter substitutions; a step that fails, or the project's verify gate once every step has succeeded, hands over to the model if a provider is configured, otherwise the run exits non-zero")
		themeFlag    = fs.String("theme", "", "colour theme for this run: auto, dark, light, or the name of a builtin or user theme (--inspect themes lists them)")
		web          = fs.Bool("web", false, "serve the session in a browser instead of the terminal")
		webAddr      = fs.String("web-addr", "127.0.0.1:8787", "listen address for --web. Loopback by default; anyone who can reach the port can approve commands in this directory")
		bg           = fs.Bool("bg", false, "run the prompt in the background, print the session id, and return; reopen with --resume <id>")
		addDir       stringList
	)
	fs.Var(&addDir, "add-dir", "additional local directory file tools may read and edit (repeatable)")
	// The flag set is published so the catalogue tests can walk exactly
	// the flags this program declares. Declaring them in a helper instead
	// would mean renaming forty-odd locals whose names (version, model,
	// resume) collide with package-level ones — a rename with nothing to
	// gain but a tidier test.
	declaredFlags.Store(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *continueC {
		*cont = true
	}
	if *resumeR != "" {
		*resume = *resumeR
	}
	if *showVersion {
		fmt.Println("janetcode", version)
		return nil
	}
	if *initCfg {
		p, err := config.WriteExample()
		if err != nil {
			return err
		}
		fmt.Println("wrote", p)
		return nil
	}
	if *listSess {
		return printSessions()
	}
	if *runDoctor {
		return doctor(os.Stdout, config.Overrides{Provider: *provider, Model: *model, Effort: *effort, Theme: *themeFlag})
	}
	if *runProof {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return runProofFlag(os.Stdout, cwd, strings.TrimSpace(strings.Join(fs.Args(), " ")))
	}
	initial := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if initial != "" && *promptText != "" {
		return fmt.Errorf("%w: give the prompt either with -p or as an argument, not both", headless.ErrBadInput)
	}
	if *until != "" && *promptText == "" {
		return fmt.Errorf("%w: --until needs a headless prompt (-p)", headless.ErrBadInput)
	}
	format, err := headless.ParseFormat(*outFormat)
	if err != nil {
		return fmt.Errorf("%w: %v", headless.ErrBadInput, err)
	}
	if format != headless.FormatText && *promptText == "" && *watchCmd == "" {
		return fmt.Errorf("%w: --output-format applies to a headless run (-p)", headless.ErrBadInput)
	}
	if *watchCmd != "" && *until != "" {
		return fmt.Errorf("%w: --watch and --until cannot be combined", headless.ErrBadInput)
	}
	if *web && *acpMode {
		return fmt.Errorf("%w: --web and --acp cannot be combined", headless.ErrBadInput)
	}
	if *web && *promptText != "" {
		return fmt.Errorf("%w: --web is a session, not a one-shot; drop -p", headless.ErrBadInput)
	}
	if *bg && *web {
		return fmt.Errorf("%w: --bg and --web cannot be combined", headless.ErrBadInput)
	}
	if *bg && *acpMode {
		return fmt.Errorf("%w: --bg and --acp cannot be combined", headless.ErrBadInput)
	}
	if *recipeName != "" {
		switch {
		case *promptText != "":
			return fmt.Errorf("%w: --recipe and -p do not combine; a recipe hands over to the model on its own if a step fails", headless.ErrBadInput)
		case *watchCmd != "":
			return fmt.Errorf("%w: --recipe and --watch do not combine", headless.ErrBadInput)
		case *web:
			return fmt.Errorf("%w: --web is a session, not a one-shot; drop --recipe", headless.ErrBadInput)
		case *acpMode:
			return fmt.Errorf("%w: --recipe and --acp do not combine", headless.ErrBadInput)
		case *bg:
			return fmt.Errorf("%w: --recipe and --bg do not combine", headless.ErrBadInput)
		}
	}

	if *evalTrain != "" {
		cfgPath, _ := config.Path()
		return trainCritic(strings.Split(*evalTrain, ","), filepath.Join(filepath.Dir(cfgPath), "critic.json"), os.Stdout)
	}
	cfg, cerr := config.Load(config.Overrides{Provider: *provider, Model: *model, Effort: *effort, Theme: *themeFlag, AutoApprove: *yolo})
	err = cerr
	// Interactive first run: open the TUI and ask for a provider instead
	// of failing. Headless runs still need working credentials.
	needsConnect := err != nil && errors.Is(err, config.ErrNoCredentials) && *promptText == "" && term.IsTerminal(int(os.Stdin.Fd()))
	// Inspecting the configuration is what you reach for when the
	// configuration is the problem, so it must not need a working one.
	// The subjects that describe a running session are unavailable, and
	// the listing says which those are.
	if *inspectWhat != "" && err != nil && errors.Is(err, config.ErrNoCredentials) {
		r := inspect.New()
		cfgPath, _ := config.Path()
		cwd, _ := os.Getwd()
		registerOfflineSubjects(r, cfg, cfgPath, cwd, err)
		registerFeatureSubject(r, buildCatalog(fs, nil))
		registerThemeSubject(r, cfg)
		return inspectOnce(r, *inspectWhat, os.Stdout)
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
	if err == nil && *promptText != "" {
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
	if *maxTurns > 0 {
		cfg.MaxIterations = *maxTurns
	}
	if *compactAt > 0 {
		cfg.CompactAt = *compactAt
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if *wtBranch != "" {
		wt, err := worktree.Open(context.Background(), cwd, *wtBranch)
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
	if *replayPath != "" {
		return runReplay(*replayPath)
	}
	// Re-reading saved results needs no provider and no money: the data is
	// already on disk, and a comparison that was printed once should be
	// printable again when the statistics behind it change.
	if *evalReport != "" {
		return reportEval(*evalReport, *evalCompare)
	}
	if *evalPath != "" {
		if prov == nil {
			return errors.New("--eval needs a configured provider")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ov, err := prompt.LoadOverride(os.Getenv, *appendSystem)
		if err != nil {
			return err
		}
		return runEval(ctx, cfg, prov, evalOptions{path: *evalPath, jobs: *evalJobs, only: *evalOnly, jsonOut: *evalJSON, keep: *evalKeep,
			runs: *evalRuns, compare: *evalCompare, off: *evalOff, label: *evalLabel, prompt: ov,
			timeout: *evalTimeout, maxTurns: *evalTurns, noToolLimit: *evalNoLimit, compactAt: *evalCompact, facts: *evalFacts, factsBlind: *evalBlind})
	}

	sessDir, err := session.Dir()
	if err != nil {
		return err
	}
	var (
		sess     *session.Session
		history  []llm.Message
		resumeID = *resume
	)
	if *cont && resumeID == "" {
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
	if *bg {
		prompt := strings.TrimSpace(*promptText)
		if prompt == "" {
			prompt = initial
		}
		if prompt == "" {
			return fmt.Errorf("%w: --bg needs a prompt (-p or an argument)", headless.ErrBadInput)
		}
		return launchBackground(sess, prompt, addDir)
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
	promptOv, err := prompt.LoadOverride(os.Getenv, *appendSystem)
	if err != nil {
		return err
	}
	typed := &tool.URLSet{}
	typed.Note(initial)
	typed.Note(*promptText)
	deps := agentDeps{prov: prov, cfg: cfg, cwd: cwd, cfgDir: cfgDir, sessDir: sessDir, mem: mem, jobs: jobs, version: version, readOnly: *readonly, autoEdits: *autoEdits, autoMode: *autoMode, sandboxOn: *sandboxOn, models: pool.For, promptOverride: promptOv, typed: typed, running: &tool.Running{}, extraDirs: addDir}
	built, err := buildAgent(deps, sess, history)
	if err != nil {
		return err
	}
	defer built.close()
	ag, task, diag, repo := built.agent, built.task, built.diag, built.repo
	archive, skills := built.archive, built.skills
	_ = task
	_ = repo

	servers := mcp.Manager{Options: mcp.Options{Root: cwd, Version: version}, Registry: ag.Tools}
	defer servers.Close()
	projectServers, projectHash, err := mcp.LoadProject(cwd)
	if err != nil {
		return err
	}
	trust := mcp.TrustStore{Path: filepath.Join(cfgDir, "mcp-trust.json")}
	tokens := &mcp.TokenStore{Path: filepath.Join(cfgDir, "mcp-auth.json")}
	servers.Options.Tokens = tokens
	if *mcpAuth != "" {
		return authorizeMCP(*mcpAuth, cfg.MCPServers, projectServers, tokens)
	}
	hooksCfg, hooksHash, err := hook.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project hooks ignored)\n", err)
	}
	hooksTrusted := hooksCfg.Len() > 0 && trust.Trusted(mcp.HooksTrustKey(cwd), hooksHash)
	ag.Hooks = &hook.Runner{Config: cfg.Hooks, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
	if hooksTrusted {
		var hookNotes []string
		ag.Hooks.Config, hookNotes = hook.MergeScoped(cfg.Hooks, hooksCfg)
		for _, n := range hookNotes {
			fmt.Fprintf(os.Stderr, "[hooks] %s\n", n)
		}
	} else if hooksCfg.Len() > 0 && *promptText != "" {
		fmt.Fprintf(os.Stderr, "[hook] %s is not trusted yet; open the TUI and run /hooks trust\n", hook.ProjectFile)
	}
	verifyCfg, verifyHash, err := verify.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project verification ignored)\n", err)
	}
	verifyDefined := len(verifyCfg.Commands) > 0 || verifyCfg.Mutate != ""
	verifyTrusted := verifyDefined && trust.Trusted(mcp.VerifyTrustKey(cwd), verifyHash)
	if verifyTrusted {
		ag.Verify = &verify.Runner{Config: verifyCfg, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[verify] %s\n", s) }}
	} else if verifyDefined && *promptText != "" {
		fmt.Fprintf(os.Stderr, "[verify] %s is not trusted yet; open the TUI and run /verify trust\n", verify.ProjectFile)
	}
	rules, _ := cfg.Permissions.Rules() // validated by config.Load
	projRules, permHash, err := perm.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project permissions ignored)\n", err)
	}
	permTrusted := len(projRules.Allow) > 0 && trust.Trusted(mcp.PermissionsTrustKey(cwd), permHash)
	if permTrusted {
		rules = perm.Merge(rules, projRules)
	} else {
		rules = perm.Merge(rules, perm.Rules{Deny: projRules.Deny}) // deny rules need no trust
		if len(projRules.Allow) > 0 && *promptText != "" {
			fmt.Fprintf(os.Stderr, "[permissions] %s has allow rules that are not trusted yet; open the TUI and run /permissions trust\n", perm.ProjectFile)
		}
	}
	for _, spec := range []struct {
		flag string
		v    perm.Verdict
	}{{*allow, perm.Allowed}, {*deny, perm.Denied}} {
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
	projectTrusted := len(projectServers) > 0 && trust.Trusted(cwd, projectHash)

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
	if *inspectWhat != "" {
		return inspectOnce(subjects, *inspectWhat, os.Stdout)
	}

	if *recipeName != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if sess != nil {
			defer func() { _ = sess.Sync() }()
		}
		r, err := recipe.Load(cwd, *recipeName)
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

	if *acpMode {
		// The editor owns stdout: only ACP frames may go there, so every
		// other message in this mode is already on stderr.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		first := built
		used := false
		srv := acp.New(acp.Options{
			In: os.Stdin, Out: os.Stdout, Log: os.Stderr, Version: version,
			NewAgent: func(sessionCwd, resume string) (*agent.Agent, *session.Session, error) {
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
					return first.agent, s, nil
				}
				used = true
				d := deps
				d.cwd = root
				b, err := buildAgent(d, s, hist)
				if err != nil {
					return nil, nil, err
				}
				return b.agent, s, nil
			},
		})
		fmt.Fprintf(os.Stderr, "janetcode %s serving ACP on stdio (%s/%s in %s)\n", version, cfg.Provider, cfg.Model, cwd)
		return srv.Serve(ctx)
	}

	if *promptText != "" || *watchCmd != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		text, atts := prompt.ExpandMentions(cwd, *promptText)
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
			Until: *until, Watch: *watchCmd, Cwd: cwd, Memory: mem, AutoMemory: cfg.AutoMemory(),
			Format: format, MaxCostUSD: *maxCost,
		})
	}
	if *web {
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
			Version: version, Addr: *webAddr,
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
			Project: projectServers, ProjectHash: projectHash, ProjectTrusted: projectTrusted, Trust: trust,
			Tokens: tokens,
		},
		Hooks:    &tui.HookOptions{Global: cfg.Hooks, Project: hooksCfg, ProjectHash: hooksHash, ProjectTrusted: hooksTrusted, Trust: trust},
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
		Verify: &tui.VerifyOptions{Config: verifyCfg, ProjectHash: verifyHash, ProjectTrusted: verifyTrusted, Trust: trust},
		Perms: &tui.PermOptions{Project: projRules, ProjectHash: permHash, ProjectTrusted: permTrusted, Trust: trust,
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
