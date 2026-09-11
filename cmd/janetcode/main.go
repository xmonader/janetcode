// Command janetcode is an interactive terminal coding agent.
package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/acp"
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/llm/replay"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rates"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	_ "github.com/xmonader/janetcode/internal/treesitter" // registers the grammar parser when built with -tags treesitter
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/verify"
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
             JANETCODE_DEBUG=<file> appends a timestamped event log
Config file: $XDG_CONFIG_HOME/janetcode/config.json (default ~/.config/janetcode/config.json)
`)
	}
	var (
		promptText  = fs.String("p", "", "run this prompt headless and exit")
		outFormat   = fs.String("output-format", "text", "with -p: how the run reports itself. text prints the answer on stdout and activity on stderr; json prints one result object on stdout (answer, usage, cost and where the figure came from, turns, tool calls, permission denials, verdict, session id, duration); stream-json prints one JSON object per line as things happen, ending with that result")
		maxCost     = fs.Float64("max-cost", 0, "with -p: stop once the run has spent this much in USD (exit 55). The figure is the provider's when it reports one, otherwise list price")
		until       = fs.String("until", "", "with -p: shell command that must exit 0 before the run is done (re-loops up to 5 times)")
		watchCmd    = fs.String("watch", "", "watch mode: re-run this shell command whenever files change and have the model fix failures (use with --yolo)")
		provider    = fs.String("provider", "", "configured provider or preset name, e.g. anthropic, openai, openrouter, ollama")
		model       = fs.String("model", "", "model id (default claude-opus-5 for anthropic)")
		effort      = fs.String("effort", "", "anthropic effort: low, medium, high, xhigh, max")
		yolo        = fs.Bool("yolo", false, "auto-approve every tool call")
		readonly    = fs.Bool("readonly", false, "read-only: no edits or commands, the model can only inspect")
		continueC   = fs.Bool("c", false, "short for -continue")
		resumeR     = fs.String("r", "", "short for -resume <id>")
		autoEdits   = fs.Bool("auto-edits", false, "auto-accept file edits; commands still ask (/edits toggles it)")
		evalPath    = fs.String("eval", "", "run the eval tasks in this file or directory against the configured provider and exit")
		evalJobs    = fs.Int("eval-jobs", 2, "eval tasks to run at once")
		evalOnly    = fs.String("eval-only", "", "comma-separated task names or tags to run")
		evalJSON    = fs.String("eval-json", "", "write eval results to this JSON file")
		replayPath  = fs.String("replay", "", "re-run a saved session against the current code, with the model's replies read from the recording instead of bought: reports the tool calls it makes now against the ones recorded, and needs no API key")
		evalReport  = fs.String("eval-report", "", "re-read a saved results file and print its report and tool adoption, without running anything; with --eval-compare it prints the paired comparison of two saved files")
		evalKeep    = fs.Bool("eval-keep", false, "keep eval work directories")
		evalRuns    = fs.Int("eval-runs", 1, "repeat every eval task this many times (rates instead of coin flips). One run per task is a coin flip: this set swings 21% between identical runs, so a comparison wants 3 or more")
		evalCompare = fs.String("eval-compare", "", "results file of a baseline run to compare against (paired by task, bootstrap interval)")
		evalOff     = fs.String("eval-off", "", "comma-separated agent behaviours to disable for an ablation run: coach, syntax, loops, prune, verify, repomap, working, plan, critic")
		evalLabel   = fs.String("eval-label", "", "name recorded in the results file for this variant")
		evalTrain   = fs.String("eval-train", "", "train the trajectory critic from these eval result files (comma-separated files or directories) and exit")
		evalTimeout = fs.Duration("eval-timeout", 0, "replace every task's timeout with this wall budget (benchmarks give each harness the same)")
		evalTurns   = fs.Int("eval-max-turns", 0, "cap model calls per task run (0: the default of 50)")
		evalNoLimit = fs.Bool("eval-no-tool-limit", false, "ignore the tasks' max_tool_calls, which other harnesses do not enforce")
		inspectWhat = fs.String("inspect", "", "print one part of the session state and exit: context, events, archive, retrieval, compaction, tools, config, lsp, mcp, jobs, skills, repomap and more; `list` names them all")
		wtBranch    = fs.String("worktree", "", "run the session in a git worktree on this branch, in a checkout under .git: the real tree is untouched, two sessions can work on one repository at once, and the result is a branch to review and merge")
		acpMode     = fs.Bool("acp", false, "serve the Agent Client Protocol on stdin/stdout, for an ACP editor that launches janetcode as a subprocess (nothing but protocol messages goes to stdout)")
		evalFacts   = fs.Int("eval-facts", 0, "after each run that compacted, probe up to N facts mined from what compaction removed (exit codes, output lines, file lines) and record how many the model still recalls")
		evalBlind   = fs.Bool("eval-facts-blind", false, "probe without expand_output: what the compacted context alone retains")
		evalCompact = fs.Int("eval-compact-at", 0, "compact the history at this many estimated tokens instead of the default: a low value forces compaction on tasks that would not otherwise reach it, to measure what it costs and what it loses")
		allow       = fs.String("allow", "", "comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'")
		deny        = fs.String("deny", "", "comma-separated permission rules to refuse for this run")
		cont        = fs.Bool("continue", false, "resume the latest session for this directory")
		resume      = fs.String("resume", "", "resume the session with this id")
		showVersion = fs.Bool("version", false, "print version and exit")
		listSess    = fs.Bool("sessions", false, "list saved sessions for this directory and exit")
		initCfg     = fs.Bool("init", false, "write an example config file and exit")
		mcpAuth     = fs.String("mcp-auth", "", "log in to the named remote MCP server with OAuth (browser flow) and exit")
		runDoctor   = fs.Bool("doctor", false, "check config, credentials, a live model call, directories, MCP servers, hooks, verification, permissions, skills, memory and language servers, then exit")
	)
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
		return doctor(os.Stdout, config.Overrides{Provider: *provider, Model: *model, Effort: *effort})
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

	if *evalTrain != "" {
		cfgPath, _ := config.Path()
		return trainCritic(strings.Split(*evalTrain, ","), filepath.Join(filepath.Dir(cfgPath), "critic.json"), os.Stdout)
	}
	cfg, cerr := config.Load(config.Overrides{Provider: *provider, Model: *model, Effort: *effort, AutoApprove: *yolo})
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
		return inspectOnce(r, *inspectWhat, os.Stdout)
	}
	if err == nil {
		// Prices for models the file does not list come from LiteLLM's
		// public table, cached a day under the config directory. The
		// cache is read now; a refresh runs in the background.
		if cfgPath, perr := config.Path(); perr == nil {
			dir := filepath.Dir(cfgPath)
			cfg.RatesTable = rates.Load(dir)
			if !rates.Fresh(dir) {
				go func() {
					rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					_, _ = rates.Refresh(rctx, dir)
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
		if !errors.Is(err, config.ErrNoCredentials) {
			// A config file that cannot be used is a different thing from
			// credentials that are merely missing, and exits differently.
			return fmt.Errorf("%w: %w", headless.ErrConfig, err)
		}
		return err
	}
	var prov llm.Provider
	if !needsConnect {
		if prov, err = buildProvider(cfg); err != nil {
			return err
		}
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
		return runEval(ctx, cfg, prov, evalOptions{path: *evalPath, jobs: *evalJobs, only: *evalOnly, jsonOut: *evalJSON, keep: *evalKeep,
			runs: *evalRuns, compare: *evalCompare, off: *evalOff, label: *evalLabel,
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
	typed := &tool.URLSet{}
	typed.Note(initial)
	typed.Note(*promptText)
	deps := agentDeps{prov: prov, cfg: cfg, cwd: cwd, cfgDir: cfgDir, sessDir: sessDir, mem: mem, jobs: jobs, version: version, readOnly: *readonly, autoEdits: *autoEdits, models: pool.For, typed: typed, running: &tool.Running{}}
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
	hooksTrusted := len(hooksCfg) > 0 && trust.Trusted(hookTrustKey(cwd), hooksHash)
	ag.Hooks = &hook.Runner{Config: cfg.Hooks, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
	if hooksTrusted {
		var hookNotes []string
		ag.Hooks.Config, hookNotes = hook.MergeScoped(cfg.Hooks, hooksCfg)
		for _, n := range hookNotes {
			fmt.Fprintf(os.Stderr, "[hooks] %s\n", n)
		}
	} else if len(hooksCfg) > 0 && *promptText != "" {
		fmt.Fprintf(os.Stderr, "[hook] %s is not trusted yet; open the TUI and run /hooks trust\n", hook.ProjectFile)
	}
	verifyCfg, verifyHash, err := verify.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project verification ignored)\n", err)
	}
	verifyDefined := len(verifyCfg.Commands) > 0 || verifyCfg.Mutate != ""
	verifyTrusted := verifyDefined && trust.Trusted(cwd+"#verify", verifyHash)
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
	permTrusted := len(projRules.Allow) > 0 && trust.Trusted(cwd+"#permissions", permHash)
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
		task: task, skills: skills,
	})
	ag.Tools.Add(inspect.Tool{Registry: subjects})
	if *inspectWhat != "" {
		return inspectOnce(subjects, *inspectWhat, os.Stdout)
	}

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
		return headless.Run(ctx, headless.Options{
			Agent: ag, Session: sess, Prompt: text, Images: prompt.ImageBlocks(atts),
			Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
			Until: *until, Watch: *watchCmd, Cwd: cwd, Memory: mem, AutoMemory: cfg.AutoMemory(),
			Format: format, MaxCostUSD: *maxCost,
		})
	}
	return tui.Run(tui.Options{
		Notes: cfg.Notes,
		Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd, ConfigDir: cfgDir,
		Memory: mem, AutoMemory: cfg.AutoMemory(),
		Config: cfg, ConfigPath: cfgPath, Build: buildProvider, Connect: needsConnect,
		Resumed: len(history), Version: version, Theme: cfg.Theme,
		InitialPrompt: initial,
		MCP: &tui.MCPOptions{
			Manager: &servers, Global: cfg.MCPServers,
			Project: projectServers, ProjectHash: projectHash, ProjectTrusted: projectTrusted, Trust: trust,
			Tokens: tokens,
		},
		Hooks:     &tui.HookOptions{Global: cfg.Hooks, Project: hooksCfg, ProjectHash: hooksHash, ProjectTrusted: hooksTrusted, Trust: trust},
		LSP:       diag,
		Inspect:   subjects,
		Models:    pool.For,
		Jobs:      jobs,
		AutoTitle: true,
		Verify:    &tui.VerifyOptions{Config: verifyCfg, ProjectHash: verifyHash, ProjectTrusted: verifyTrusted, Trust: trust},
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

// buildProvider constructs the backend selected by cfg.Provider.
// providerPool builds one backend per configured provider and keeps it,
// so a session can send work to more than one model without rebuilding a
// client for every call.
type providerPool struct {
	cfg *config.Config
	mu  sync.Mutex
	by  map[string]llm.Provider
}

func newProviderPool(cfg *config.Config, active llm.Provider) *providerPool {
	p := &providerPool{cfg: cfg, by: map[string]llm.Provider{}}
	if active != nil && cfg != nil && cfg.Provider != "" {
		p.by[cfg.Provider] = active
	}
	return p
}

// For resolves a model name to the backend that serves it. The name is
// returned as well, so the caller records what actually ran.
func (p *providerPool) For(model string) (llm.Provider, string, error) {
	if p == nil || p.cfg == nil {
		return nil, "", fmt.Errorf("no providers are configured")
	}
	name, ok := p.cfg.RouteFor(model)
	if !ok {
		return nil, "", fmt.Errorf("no configured provider serves %q; add one with /connect, or route it in the config", model)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prov, ok := p.by[name]; ok {
		return prov, model, nil
	}
	// Build the named provider by copying the config with it selected.
	sub := *p.cfg
	sub.Provider = name
	prov, err := buildProvider(&sub)
	if err != nil {
		return nil, "", fmt.Errorf("%s (for model %s): %w", name, model, err)
	}
	p.by[name] = prov
	return prov, model, nil
}

func buildProvider(cfg *config.Config) (llm.Provider, error) {
	p := cfg.Active()
	switch p.Type {
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKeys:      p.Keys(),
			BaseURL:      p.BaseURL,
			Thinking:     cfg.ThinkingEnabled(),
			HideThinking: !cfg.ThinkingShown(),
			Effort:       cfg.Effort,
		})
	case "openai":
		return openai.New(openai.Config{
			APIKeys: p.Keys(),
			BaseURL: p.BaseURL,
			Name:    cfg.Provider,
		})
	}
	return nil, fmt.Errorf("provider %q has unknown type %q", cfg.Provider, p.Type)
}

type evalOptions struct {
	path, only, jsonOut string
	jobs, runs          int
	keep                bool
	timeout             time.Duration
	maxTurns            int
	noToolLimit         bool
	compactAt           int
	facts               int
	factsBlind          bool
	compare, off, label string
}

// runEval runs the eval tasks with fresh, minimal agents (core tools, no
// hooks, memory or MCP) so results reflect the model and the prompts.
func runEval(ctx context.Context, cfg *config.Config, prov llm.Provider, o evalOptions) error {
	tasks, err := eval.Load(o.path)
	if err != nil {
		return err
	}
	if o.only != "" {
		tasks = eval.Filter(tasks, strings.Split(o.only, ","))
		if len(tasks) == 0 {
			return fmt.Errorf("no tasks match %q", o.only)
		}
	}
	off := map[string]bool{}
	for _, f := range strings.Split(o.off, ",") {
		if f = strings.TrimSpace(f); f != "" {
			if !agent.KnownSwitch(f) {
				return fmt.Errorf("--eval-off: unknown behaviour %q (%s)", f, strings.Join(agent.Switches(), ", "))
			}
			off[f] = true
		}
	}
	variant := o.label
	if variant == "" && len(off) > 0 {
		variant = "off:" + o.off
	}
	var baseline *eval.File
	if o.compare != "" {
		f, err := eval.ReadFile(o.compare)
		if err != nil {
			return err
		}
		baseline = &f
	}
	runs := max(o.runs, 1)
	fmt.Fprintf(os.Stderr, "running %d eval tasks × %d with %s/%s (%d at a time)", len(tasks), runs, cfg.Provider, cfg.Model, o.jobs)
	if variant != "" {
		fmt.Fprintf(os.Stderr, " · variant %s", variant)
	}
	fmt.Fprintln(os.Stderr)
	maxIter := cfg.MaxIterations
	if o.maxTurns > 0 {
		maxIter = o.maxTurns
	}
	compactAt := cfg.CompactAt
	if o.compactAt > 0 {
		compactAt = o.compactAt
		fmt.Fprintf(os.Stderr, "compacting at ~%d tokens\n", compactAt)
	}
	// Archived tool outputs live beside the work directories, so the eval
	// agent has the same expand_output round-trip the real one does.
	archiveDir, err := os.MkdirTemp("", "janetcode-eval-archive-")
	if err != nil {
		return err
	}
	if !o.keep {
		defer os.RemoveAll(archiveDir)
	}
	r := &eval.Runner{Jobs: o.jobs, Runs: runs, Keep: o.keep, Log: os.Stderr, Timeout: o.timeout, IgnoreToolLimit: o.noToolLimit, Facts: o.facts, FactsBlind: o.factsBlind, NewAgent: func(root string) (*agent.Agent, error) {
		repo := &repomap.Cache{Root: root}
		var prep func([]llm.Block) ([]llm.Block, string)
		if !off["repomap"] {
			prep = repomap.TurnPrep(repo, 0)
		}
		archive := agent.NewArchive(filepath.Join(archiveDir, filepath.Base(root)))
		expand := &agent.ExpandOutput{Archive: archive}
		// The eval agent carries the tools a real session does, minus the
		// ones that need a terminal or the network. A benchmark run
		// against a smaller agent measures something nobody uses.
		ag := &agent.Agent{
			Provider:      prov,
			Off:           off,
			Prepare:       prep,
			PlanCheck:     repo.CheckPlan,
			Log:           &session.Memory{},
			Archive:       archive,
			Price:         cfg.PriceFor(cfg.Model),
			Tools:         evalTools(root, repo, expand, off),
			System:        prompt.System(root),
			Model:         cfg.Model,
			MaxTokens:     cfg.MaxTokens,
			MaxIterations: maxIter,
			CompactAt:     compactAt,
		}
		expand.Log = ag.LogEvent // the archive's lookups go on the same trail
		if !off["todo"] {
			ag.Tools.Add(&agent.TodoTool{Parent: ag})
		}
		if !off["script"] {
			ag.Tools.Add(&agent.Script{Parent: ag, Root: root})
		}
		// The eval agent follows the product default, so a measurement is
		// of what users actually run; --eval-off intent forces it off.
		ag.Tools.Intent = cfg.IntentEnabled() && !off["intent"]
		if !off["oversized"] {
		}
		evalToolsOnce.Do(func() { evalToolNames = ag.Tools.Names() })
		return ag, nil
	}}
	results := r.Run(ctx, tasks)
	passed := eval.Report(os.Stdout, results)
	// Which tools the model actually reached for. A tool that is never
	// called is not a feature however well it works, and only a set of
	// runs can say so.
	eval.WriteToolUse(os.Stdout, eval.Summarize(results), evalToolNames)
	if baseline != nil {
		label := baseline.Variant
		if label == "" {
			label = o.compare
		}
		fmt.Fprintf(os.Stdout, "\ncompared with %s (%s):\n", label, baseline.When.Format("2006-01-02 15:04"))
		if o.runs < 3 || baseline.Runs < 3 {
			fmt.Fprintln(os.Stdout, "note: fewer than 3 runs per task on one side; this set swings 21% between identical runs, so read the intervals, not the point estimates.")
		}
		eval.WriteComparison(os.Stdout, eval.Compare(baseline.Results, results))
	}
	if o.jsonOut != "" {
		if err := eval.WriteFile(o.jsonOut, eval.File{Model: cfg.Provider + "/" + cfg.Model, Variant: variant, Results: results}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", o.jsonOut)
	}
	if passed < len(results) {
		return fmt.Errorf("%d of %d eval tasks failed", len(results)-passed, len(results))
	}
	return nil
}

// trainCritic fits the trajectory predictor to the pass/fail outcomes in
// eval result files and saves it where the agent loads it from.
func trainCritic(paths []string, out string, w io.Writer) error {
	var samples []agent.Sample
	files := 0
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		var list []string
		if st, err := os.Stat(p); err != nil {
			return err
		} else if st.IsDir() {
			list, _ = filepath.Glob(filepath.Join(p, "*.json"))
		} else {
			list = []string{p}
		}
		for _, f := range list {
			ef, err := eval.ReadFile(f)
			if err != nil {
				return err
			}
			files++
			for _, r := range ef.Results {
				if len(r.Features) > 0 {
					samples = append(samples, agent.Sample{Features: r.Features, Passed: r.Passed})
				}
			}
		}
	}
	if len(samples) == 0 {
		return fmt.Errorf("no runs with trajectory features in %d result file(s); results written before features were recorded cannot be used", files)
	}
	p, err := agent.Train(samples)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "trajectory critic trained on %d runs (%d passed, %d failed) from %d file(s)\n", p.Samples, p.Positives, p.Negatives, files)
	fmt.Fprintf(w, "cross-validated accuracy %.0f%% (always guessing the majority: %.0f%%)\n", p.Accuracy*100, p.Baseline*100)
	if p.Accuracy <= p.Baseline {
		fmt.Fprintf(w, "not saved: the critic is no better than guessing; more failing runs are needed before it can help\n")
		return nil
	}
	if err := p.Save(out); err != nil {
		return err
	}
	fmt.Fprintf(w, "saved to %s\n", out)
	return nil
}

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

// diagnoseNote turns language-server diagnostics for a just-edited file
// into the note appended to the tool result.
// recordedCalls is the tool calls a session made, in order, read straight
// from its log.
func recordedCalls(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var e session.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == session.TypeToolCall && !strings.HasPrefix(e.CallID, "script-") {
			out = append(out, e.Name)
		}
	}
	return out, sc.Err()
}

// runReplay re-runs a recorded session with the model's part read from
// disk. Every bug fixed in this project's history that changed how a
// response is turned into tool calls would have shown up here as a
// different call sequence, for free and without a key.
func runReplay(path string) error {
	rp, err := replay.Load(path)
	if err != nil {
		return err
	}
	want, err := recordedCalls(path)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "janetcode-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	var got []string
	ag := &agent.Agent{
		Provider:    rp,
		Log:         &session.Memory{},
		AutoApprove: true,
		ReadOnly:    true, // a replay must not touch the machine
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: root}, tool.ListDir{Root: root},
			tool.Glob{Root: root}, tool.Grep{Root: root},
		),
		System:        prompt.System(root),
		MaxIterations: 200,
	}
	ctx := context.Background()
	prompts := rp.Prompts()
	fmt.Fprintf(os.Stdout, "replaying %s: %d prompt(s), %d recorded model call(s)\n", filepath.Base(path), len(prompts), rp.Remaining())
	for _, p := range prompts {
		if rp.Remaining() == 0 {
			break
		}
		err := ag.Run(ctx, p, func(e agent.Event) {
			if e.Kind == agent.EvToolRun && e.Call != nil {
				got = append(got, e.Call.Name)
			}
		})
		if err != nil && !errors.Is(err, agent.ErrMaxIterations) {
			fmt.Fprintf(os.Stdout, "  turn ended early: %v\n", err)
			break
		}
	}
	fmt.Fprintf(os.Stdout, "\nrecorded: %s\nnow:      %s\n", strings.Join(want, " "), strings.Join(got, " "))
	if strings.Join(want, " ") == strings.Join(got, " ") {
		fmt.Fprintln(os.Stdout, "\nthe same calls in the same order: no behaviour change")
		return nil
	}
	fmt.Fprintln(os.Stdout, "\nthe call sequence changed. That is a regression unless it was intended.")
	return nil
}

// reportEval prints a saved results file, and the paired comparison
// against a second one when given.
func reportEval(path, base string) error {
	f, err := eval.ReadFile(path)
	if err != nil {
		return err
	}
	label := f.Variant
	if label == "" {
		label = filepath.Base(path)
	}
	fmt.Fprintf(os.Stdout, "%s — %d result(s), %s\n\n", label, len(f.Results), f.Model)
	eval.Report(os.Stdout, f.Results)
	eval.WriteToolUse(os.Stdout, eval.Summarize(f.Results), nil)
	if base == "" {
		return nil
	}
	b, err := eval.ReadFile(base)
	if err != nil {
		return err
	}
	blabel := b.Variant
	if blabel == "" {
		blabel = filepath.Base(base)
	}
	fmt.Fprintf(os.Stdout, "\ncompared with %s (%s):\n", blabel, b.When.Format("2006-01-02 15:04"))
	eval.WriteComparison(os.Stdout, eval.Compare(b.Results, f.Results))
	return nil
}

// evalToolNames is what the eval agent was given, so the adoption report
// can name the tools nothing ever called.
var (
	evalToolNames []string
	evalToolsOnce sync.Once
)

// evalTools is the eval agent's registry. --eval-off extratools strips it
// to the four tools a session actually leans on — read, write, edit and a
// shell — so that the cost of a wide tool surface can be measured rather
// than assumed: every schema is in the prefix of every request, and every
// tool is one more thing to choose between each turn.
func evalTools(root string, repo *repomap.Cache, expand *agent.ExpandOutput, off map[string]bool) *tool.Registry {
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

// envOff reads JANETCODE_OFF, a comma-separated list of behaviours to
// disable for this session.
func envOff() map[string]bool {
	out := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("JANETCODE_OFF"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
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

// hookTrustKey keeps hook approvals apart from MCP approvals in the same
// trust store.
func hookTrustKey(cwd string) string { return cwd + "#hooks" }

// authorizeMCP runs the OAuth flow for a remote server from the command
// line, printing the URL in case no browser can be opened.
func authorizeMCP(name string, global, project map[string]mcp.ServerConfig, tokens *mcp.TokenStore) error {
	cfg, ok := global[name]
	if !ok {
		cfg, ok = project[name]
	}
	if !ok {
		return fmt.Errorf("no MCP server named %q in the config or %s", name, mcp.ProjectFile)
	}
	if cfg.URL == "" {
		return fmt.Errorf("mcp %s runs locally; only remote servers use OAuth", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := mcp.Authorize(ctx, mcp.AuthOptions{
		ServerURL: cfg.URL, Store: tokens,
		ClientID: cfg.ClientID, ClientSecret: os.ExpandEnv(cfg.ClientSecret), Scope: cfg.Scope,
		OpenURL: mcp.OpenBrowser,
		Notify:  func(s string) { fmt.Fprintln(os.Stderr, "[mcp auth] "+s) },
	})
	if err != nil {
		return fmt.Errorf("mcp auth %s: %w", name, err)
	}
	fmt.Printf("mcp %s: authorized; token stored in %s\n", name, tokens.Path)
	return nil
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
	// typed holds the hosts the user named themselves. fetch_url reaches a
	// private-network address only for a host that is in here.
	typed *tool.URLSet
	// running is the registry of foreground commands that can be moved to
	// the background while they run.
	running *tool.Running
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
}

// close releases what the agent held. It is the caller's job, since the
// pieces outlive any single turn.
func (b builtAgent) close() {
	b.task.Close(context.Background())
	b.diag.Close()
}

// buildAgent wires one agent for a session: tools rooted at the project,
// the system prompt, skills and memory, diagnostics, the repository map
// and the trajectory critic.
func buildAgent(d agentDeps, sess *session.Session, history []llm.Message) (builtAgent, error) {
	task := &agent.Task{Root: d.cwd, Models: d.models}
	jobs := d.jobs
	cfgDir := d.cfgDir
	archive := agent.NewArchive(session.ArchiveDir(d.sessDir, d.cwd))
	mem := d.mem
	seen := &tool.Seen{}
	expand := &agent.ExpandOutput{Archive: archive}
	ag := &agent.Agent{
		Provider: d.prov,
		Archive:  archive,
		Log:      sess,
		Price:    d.cfg.PriceFor(d.cfg.Model),
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: d.cwd, Seen: seen},
			tool.WriteFile{Root: d.cwd, Seen: seen},
			tool.EditFile{Root: d.cwd, Seen: seen, NoLines: envOff()["lineedit"]},
			tool.Bash{Root: d.cwd, Cache: &tool.ShellCache{}, Jobs: jobs, Running: d.running},
			tool.JobTool{Jobs: jobs},
			tool.FetchURL{Typed: d.typed},
			tool.WebSearch{Config: d.cfg.Search},
			expand,
			task,
		),
		System:        prompt.System(d.cwd),
		Model:         d.cfg.Model,
		MaxTokens:     d.cfg.MaxTokens,
		MaxIterations: d.cfg.MaxIterations,
		AutoApprove:   d.cfg.AutoApprove,
		AutoEdits:     d.autoEdits,
		ReadOnly:      d.readOnly,
		CompactAt:     d.cfg.CompactAt,
		Messages:      history,
		Pins:          sess.Meta.Pins,
		Jobs:          jobs,
		ContextWindow: d.cfg.MetaFor(d.cfg.Model).ContextWindow,
	}
	expand.Log = ag.LogEvent // the archive's lookups go on the same trail
	ag.RestoreFromLog()      // plan mode and the turn counter of a resumed session
	task.Parent = ag
	ag.Tools.Add(&agent.GoalStatus{Parent: ag})
	ag.Tools.Add(&agent.TodoTool{Parent: ag})
	// JANETCODE_OFF is the ablation switch a session has: the eval runner
	// takes -eval-off, and a real session takes the same names here, so a
	// behaviour can be turned off without a rebuild.
	if off := envOff(); !off["script"] {
		ag.Tools.Add(&agent.Script{Parent: ag, Root: d.cwd})
	}
	skills := func() []skill.Skill { return skill.Load(d.cwd, cfgDir) }
	task.Profiles = func() []skill.Profile { return skill.LoadProfiles(d.cwd, cfgDir) }
	ag.Tools.Add(&skill.Tool{Skills: skills})
	ag.Tools.Add(&memory.Tool{Store: mem})
	diag := lsp.New(d.cwd, d.cfg.LSP)
	if diag.Enabled() {
		ag.Tools.Add(lsp.Tool{M: diag})
		ag.Tools.Add(lsp.SymbolTool{M: diag})
		ag.Diagnose = diagnoseNote(diag)
	}
	ag.SystemExtra = func() string { return mem.Index() + skill.Index(skills()) }
	repo := &repomap.Cache{Root: d.cwd}
	// The search tools. On a measured fan-out set, offering these
	// alongside bash cost 23% more context (x1.23 [1.10, 1.44]) for the
	// same pass rate, because the model takes many small steps instead of
	// writing one pipeline — see docs/benchmarks/toolsurface. lean_tools
	// defers them behind tool_search, where they stay reachable for the
	// case that measurement does not cover: a large unfamiliar repository.
	search := []tool.Tool{
		tool.ListDir{Root: d.cwd},
		tool.Glob{Root: d.cwd},
		tool.Grep{Root: d.cwd},
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
	var err error
	if ag.Predictor, err = agent.LoadPredictor(filepath.Join(cfgDir, "critic.json")); err != nil {
		fmt.Fprintf(os.Stderr, "trajectory critic not loaded: %v\n", err)
	}
	ag.Prepare = chainPrep(skill.TurnPrep(&skill.Injector{Skills: skills}, mem), repomap.TurnPrep(repo, 0))
	ag.Tools.Intent = d.cfg.IntentEnabled()
	ag.Tools.Add(&tool.ToolSearch{Registry: ag.Tools, ContextWindow: ag.ContextWindow})
	ag.UndoPath = sess.UndoPath()
	if snaps, serr := snapshot.Open(d.cwd); serr == nil {
		ag.Snapshots = snaps
	}
	if err := ag.LoadUndo(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	return builtAgent{agent: ag, task: task, diag: diag, repo: repo, archive: archive, skills: skills}, nil
}
