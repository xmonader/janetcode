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
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/verify"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "janetcode:", err)
		os.Exit(1)
	}
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
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Environment: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL
             (a key may be a comma-separated list; they are rotated on rate limits),
             JANETCODE_PROVIDER, JANETCODE_MODEL, JANETCODE_EFFORT, JANETCODE_THEME
             JANETCODE_DEBUG=<file> appends a timestamped event log
Config file: $XDG_CONFIG_HOME/janetcode/config.json (default ~/.config/janetcode/config.json)
`)
	}
	var (
		promptText  = fs.String("p", "", "run this prompt headless and exit")
		until       = fs.String("until", "", "with -p: shell command that must exit 0 before the run is done (re-loops up to 5 times)")
		watchCmd    = fs.String("watch", "", "watch mode: re-run this shell command whenever files change and have the model fix failures (use with --yolo)")
		provider    = fs.String("provider", "", "configured provider or preset name, e.g. anthropic, openai, openrouter, ollama")
		model       = fs.String("model", "", "model id (default claude-opus-5 for anthropic)")
		effort      = fs.String("effort", "", "anthropic effort: low, medium, high, xhigh, max")
		yolo        = fs.Bool("yolo", false, "auto-approve every tool call")
		readonly    = fs.Bool("readonly", false, "read-only: no edits or commands, the model can only inspect")
		evalPath    = fs.String("eval", "", "run the eval tasks in this file or directory against the configured provider and exit")
		evalJobs    = fs.Int("eval-jobs", 2, "eval tasks to run at once")
		evalOnly    = fs.String("eval-only", "", "comma-separated task names or tags to run")
		evalJSON    = fs.String("eval-json", "", "write eval results to this JSON file")
		evalKeep    = fs.Bool("eval-keep", false, "keep eval work directories")
		allow       = fs.String("allow", "", "comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'")
		deny        = fs.String("deny", "", "comma-separated permission rules to refuse for this run")
		cont        = fs.Bool("continue", false, "resume the latest session for this directory")
		resume      = fs.String("resume", "", "resume the session with this id")
		showVersion = fs.Bool("version", false, "print version and exit")
		listSess    = fs.Bool("sessions", false, "list saved sessions for this directory and exit")
		initCfg     = fs.Bool("init", false, "write an example config file and exit")
		runDoctor   = fs.Bool("doctor", false, "check config, credentials, a live model call, directories, MCP servers, hooks, verification, permissions, skills, memory and language servers, then exit")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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
		return errors.New("give the prompt either with -p or as an argument, not both")
	}
	if *until != "" && *promptText == "" {
		return errors.New("--until needs a headless prompt (-p)")
	}
	if *watchCmd != "" && *until != "" {
		return errors.New("--watch and --until cannot be combined")
	}

	cfg, err := config.Load(config.Overrides{Provider: *provider, Model: *model, Effort: *effort, AutoApprove: *yolo})
	// Interactive first run: open the TUI and ask for a provider instead
	// of failing. Headless runs still need working credentials.
	needsConnect := err != nil && errors.Is(err, config.ErrNoCredentials) && *promptText == "" && term.IsTerminal(int(os.Stdin.Fd()))
	if err != nil && !needsConnect {
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
	if *evalPath != "" {
		if prov == nil {
			return errors.New("--eval needs a configured provider")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runEval(ctx, cfg, prov, evalOptions{path: *evalPath, jobs: *evalJobs, only: *evalOnly, jsonOut: *evalJSON, keep: *evalKeep})
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

	task := &agent.Task{Root: cwd}
	jobs := &tool.Jobs{}
	defer jobs.KillAll()
	cfgPath, _ := config.Path()
	cfgDir := filepath.Dir(cfgPath)
	archive := agent.NewArchive(session.ArchiveDir(sessDir, cwd))
	mem := memory.Open(filepath.Dir(sessDir), cwd)
	if em, ok := prov.(llm.Embedder); ok && cfg.EmbeddingModel != "" {
		model := cfg.EmbeddingModel
		mem.Embed = func(ctx context.Context, texts []string) ([][]float32, error) { return em.Embed(ctx, model, texts) }
	}
	ag := &agent.Agent{
		Provider: prov,
		Archive:  archive,
		Price:    cfg.PriceFor(cfg.Model),
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: cwd},
			tool.WriteFile{Root: cwd},
			tool.EditFile{Root: cwd},
			tool.ListDir{Root: cwd},
			tool.Glob{Root: cwd},
			tool.Grep{Root: cwd},
			tool.Bash{Root: cwd, Cache: &tool.ShellCache{}, Jobs: jobs},
			tool.JobTool{Jobs: jobs},
			tool.FetchURL{},
			tool.WebSearch{Config: cfg.Search},
			&agent.ExpandOutput{Archive: archive},
			task,
		),
		System:        prompt.System(cwd),
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		MaxIterations: cfg.MaxIterations,
		AutoApprove:   cfg.AutoApprove,
		ReadOnly:      *readonly,
		CompactAt:     cfg.CompactAt,
		Messages:      history,
		Pins:          sess.Meta.Pins,
		Jobs:          jobs,
		ContextWindow: cfg.MetaFor(cfg.Model).ContextWindow,
	}
	task.Parent = ag
	skills := func() []skill.Skill { return skill.Load(cwd, cfgDir) }
	task.Profiles = func() []skill.Profile { return skill.LoadProfiles(cwd, cfgDir) }
	ag.Tools.Add(&skill.Tool{Skills: skills})
	ag.Tools.Add(&memory.Tool{Store: mem})
	diag := lsp.New(cwd, cfg.LSP)
	defer diag.Close()
	if diag.Enabled() {
		ag.Tools.Add(lsp.Tool{M: diag})
		ag.Diagnose = diagnoseNote(diag)
	}
	ag.SystemExtra = func() string { return mem.Index() + skill.Index(skills()) }
	ag.Prepare = skill.TurnPrep(&skill.Injector{Skills: skills}, mem)
	ag.Tools.Add(&tool.ToolSearch{Registry: ag.Tools})
	ag.UndoPath = sess.UndoPath()
	if repo, err := snapshot.Open(cwd); err == nil {
		ag.Snapshots = repo
	}
	if err := ag.LoadUndo(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	servers := mcp.Manager{Options: mcp.Options{Root: cwd, Version: version}, Registry: ag.Tools}
	defer servers.Close()
	projectServers, projectHash, err := mcp.LoadProject(cwd)
	if err != nil {
		return err
	}
	trust := mcp.TrustStore{Path: filepath.Join(cfgDir, "mcp-trust.json")}
	hooksCfg, hooksHash, err := hook.LoadProject(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project hooks ignored)\n", err)
	}
	hooksTrusted := len(hooksCfg) > 0 && trust.Trusted(hookTrustKey(cwd), hooksHash)
	ag.Hooks = &hook.Runner{Config: cfg.Hooks, Cwd: cwd, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
	if hooksTrusted {
		ag.Hooks.Config = hook.Merge(cfg.Hooks, hooksCfg)
	} else if len(hooksCfg) > 0 && *promptText != "" {
		fmt.Fprintf(os.Stderr, "[hook] %s is not trusted yet; open the TUI and run /hooks trust\n", hook.ProjectFile)
	}
	verifyCfg, verifyHash, err := verify.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (project verification ignored)\n", err)
	}
	verifyTrusted := len(verifyCfg.Commands) > 0 && trust.Trusted(cwd+"#verify", verifyHash)
	if verifyTrusted {
		ag.Verify = &verify.Runner{Config: verifyCfg, Cwd: cwd}
	} else if len(verifyCfg.Commands) > 0 && *promptText != "" {
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
		})
	}
	return tui.Run(tui.Options{
		Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd, ConfigDir: cfgDir,
		Memory: mem, AutoMemory: cfg.AutoMemory(),
		Config: cfg, ConfigPath: cfgPath, Build: buildProvider, Connect: needsConnect,
		Resumed: len(history), Version: version, Theme: cfg.Theme,
		InitialPrompt: initial,
		MCP: &tui.MCPOptions{
			Manager: &servers, Global: cfg.MCPServers,
			Project: projectServers, ProjectHash: projectHash, ProjectTrusted: projectTrusted, Trust: trust,
		},
		Hooks:     &tui.HookOptions{Global: cfg.Hooks, Project: hooksCfg, ProjectHash: hooksHash, ProjectTrusted: hooksTrusted, Trust: trust},
		LSP:       diag,
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
func buildProvider(cfg *config.Config) (llm.Provider, error) {
	p := cfg.Active()
	switch p.Type {
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKeys:  p.Keys(),
			BaseURL:  p.BaseURL,
			Thinking: cfg.ThinkingEnabled(),
			Effort:   cfg.Effort,
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
	jobs                int
	keep                bool
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
	fmt.Fprintf(os.Stderr, "running %d eval tasks with %s/%s (%d at a time)\n", len(tasks), cfg.Provider, cfg.Model, o.jobs)
	r := &eval.Runner{Jobs: o.jobs, Keep: o.keep, Log: os.Stderr, NewAgent: func(root string) (*agent.Agent, error) {
		return &agent.Agent{
			Provider: prov,
			Price:    cfg.PriceFor(cfg.Model),
			Tools: tool.NewRegistry(
				tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, tool.EditFile{Root: root},
				tool.ListDir{Root: root}, tool.Glob{Root: root}, tool.Grep{Root: root},
				tool.Bash{Root: root, Cache: &tool.ShellCache{}},
			),
			System:        prompt.System(root),
			Model:         cfg.Model,
			MaxTokens:     cfg.MaxTokens,
			MaxIterations: cfg.MaxIterations,
			CompactAt:     cfg.CompactAt,
		}, nil
	}}
	results := r.Run(ctx, tasks)
	passed := eval.Report(os.Stdout, results)
	if o.jsonOut != "" {
		if err := eval.WriteJSON(o.jsonOut, results); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", o.jsonOut)
	}
	if passed < len(results) {
		return fmt.Errorf("%d of %d eval tasks failed", len(results)-passed, len(results))
	}
	return nil
}

// diagnoseNote turns language-server diagnostics for a just-edited file
// into the note appended to the tool result.
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
