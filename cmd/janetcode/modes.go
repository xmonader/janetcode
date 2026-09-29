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
	"syscall"

	"github.com/xmonader/janetcode/internal/acp"
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/cua"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tui"
	"github.com/xmonader/janetcode/internal/webui"
	"golang.org/x/term"
)

// sessionMode is one way a fully wired session can end.
type sessionMode struct {
	name string
	// when reports whether this run is that mode. The last row's when
	// always holds: the TUI is what is left when nothing else applies.
	when func(*options) bool
	// hints says whether the mode opens the two per-user records first.
	// The one-shot modes do not: --inspect prints a subject and
	// --recipe replays tool calls, and neither is a session anyone is
	// being taught anything about.
	hints bool
	run   func(*root) error
}

// sessionModes is the dispatch table, in priority order: the first row
// whose condition holds is the run. It is a table rather than nested ifs
// because the order is the meaning -- --inspect beats everything, the
// TUI is the fallthrough -- and because a test can then walk it.
var sessionModes = []sessionMode{
	{"inspect", func(o *options) bool { return o.inspectWhat != "" }, false,
		func(r *root) error { return inspectOnce(r.subjects, r.opts.inspectWhat, os.Stdout, false) }},
	{"recipe", func(o *options) bool { return o.recipeName != "" }, false, (*root).recipeMode},
	{"acp", func(o *options) bool { return o.acpMode }, true, (*root).acpMode},
	{"headless", func(o *options) bool { return o.promptText != "" || o.watchCmd != "" }, true, (*root).headlessMode},
	{"web", func(o *options) bool { return o.web }, true, (*root).webMode},
	{"tui", func(*options) bool { return true }, true, (*root).tuiMode},
}

// dispatch runs the mode this invocation asked for.
func (r *root) dispatch() error {
	for _, m := range sessionModes {
		if !m.when(r.opts) {
			continue
		}
		if m.hints {
			r.openHintsAndUsage()
		}
		return m.run(r)
	}
	// Unreachable: the table's last row matches everything, and a test
	// holds it to that.
	return fmt.Errorf("no mode applies to this invocation")
}

// startMCP starts the servers this run should have: the configured ones,
// plus the project's when its file is trusted. warnUntrusted says whether
// an untrusted project file is worth a line on stderr.
func (r *root) startMCP(ctx context.Context, warnUntrusted bool) {
	toStart := map[string]mcp.ServerConfig{}
	for n, c := range r.cfg.MCPServers {
		toStart[n] = c
	}
	if r.projectTrusted {
		for n, c := range r.projectServers {
			toStart[n] = c
		}
	} else if warnUntrusted && len(r.projectServers) > 0 {
		fmt.Fprintf(os.Stderr, "[mcp] %s lists servers that are not trusted yet; open the TUI and run /mcp trust\n", mcp.ProjectFile)
	}
	if r.cfg.ComputerUseEnabled() {
		if _, ok := cua.MergeInto(toStart); !ok {
			fmt.Fprintf(os.Stderr, "[cua] computer_use is on but no driver is on PATH\n%s\n", cua.InstallHint)
		}
	}
	r.servers.StartAll(ctx, toStart, func(name string, c *mcp.Client, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] %s failed: %v\n", name, err)
			return
		}
		fmt.Fprintf(os.Stderr, "[mcp] %s ready: %d tools\n", name, len(c.Tools()))
	})
}

// syncSession flushes the session record. Appending syncs on a timer, not
// per event; a run with no settling step forces the last of it out here.
func (r *root) syncSession() {
	if r.sess != nil {
		_ = r.sess.Sync()
	}
}

// openHintsAndUsage opens the two per-user records an interactive or
// headless session keeps: what the hint engine has already said, and
// which features have been reached for.
func (r *root) openHintsAndUsage() {
	cfg, cfgDir, cwd := r.cfg, r.cfgDir, r.cwd
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
	r.onClose(func() { _ = usage.Flush() })
	r.fs.Visit(func(f *flag.Flag) { usage.Note(f.Name) })
	r.hints, r.usage = hintEngine, usage
}

// recipeMode replays a saved recipe as tool calls, with no model call.
func (r *root) recipeMode() error {
	o, ag, cwd := r.opts, r.ag, r.cwd
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer r.syncSession()
	rec, err := recipe.Load(cwd, o.recipeName)
	if errors.Is(err, recipe.ErrNoRecipe) {
		return headless.BadInput("--recipe: %v", err)
	}
	if err != nil {
		return fmt.Errorf("--recipe: %w", err)
	}
	if !r.gate.Exec(cwd, r.gate == nil) {
		return headless.BadInput("--recipe: this project's recipes run once you trust it: add --trust full, or run /trust full in a session (project trust is %s)", r.gate.State(cwd).Level)
	}
	// A recipe is built through the same agent the normal headless
	// run (-p) uses, so its steps see the same tools — including a
	// configured MCP server's, when the recipe actually calls one.
	// Starting every server for a recipe that never touches one would
	// slow down the common case (plain file and shell steps) for
	// nothing, so this only starts them when a step's tool name says
	// otherwise; the manager is closed by the root either way.
	if agent.NeedsMCP(rec) {
		r.startMCP(ctx, true)
	}
	return runRecipeFlag(ctx, ag, cwd, rec, r.fs.Args(), os.Stdout, os.Stderr, os.Stdin)
}

// acpMode serves the Agent Client Protocol on stdin/stdout.
func (r *root) acpMode() error {
	cfg, cwd, sessDir, sess, history, deps, built := r.cfg, r.cwd, r.sessDir, r.sess, r.history, r.deps, r.built
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

// headlessMode runs one prompt (-p), or --watch's fix-on-save loop.
func (r *root) headlessMode() error {
	o, cfg, ag, cwd, mem, sess, format := r.opts, r.cfg, r.ag, r.cwd, r.mem, r.sess, r.format
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	text, atts := prompt.ExpandMentions(cwd, o.promptText)
	for _, a := range atts {
		fmt.Fprintf(os.Stderr, "[attached] %s\n", a.Path)
	}
	if extra := pipedStdin(); extra != "" {
		text += "\n\n<stdin>\n" + extra + "\n</stdin>"
	}
	r.startMCP(ctx, true)
	if line := trustNotice(r.gate, cwd); line != "" {
		fmt.Fprintln(os.Stderr, line)
	}
	defer r.syncSession()
	finish := startBgRun(ag, sess, cwd)
	err := headless.Run(ctx, headless.Options{
		Agent: ag, Session: sess, Prompt: text, Images: prompt.ImageBlocks(atts),
		Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
		Until: o.until, Watch: o.watchCmd, Cwd: cwd, Memory: mem, AutoMemory: cfg.AutoMemory(),
		Format: format, MaxCostUSD: o.maxCost,
	})
	finish(err)
	return err
}

// webMode serves the session in a browser instead of the terminal.
func (r *root) webMode() error {
	o, cfg, ag, cwd, cfgDir, cfgPath := r.opts, r.cfg, r.ag, r.cwd, r.cfgDir, r.cfgPath
	sess, mem, jobs, subjects, catalog, typed, pool, built := r.sess, r.mem, r.jobs, r.subjects, r.catalog, r.typed, r.pool, r.built
	mcpOpts := &webui.MCPOptions{
		Manager: &r.servers, Global: cfg.MCPServers,
		Project: r.projectServers, ProjectHash: r.projectHash, Trust: r.trust,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r.startMCP(ctx, false)
	defer r.syncSession()
	return webui.Run(ctx, webui.Options{
		TrustGate: r.gate,
		Agent:     ag, Session: sess, Provider: cfg.Provider, Cwd: cwd,
		Version: version, Addr: o.webAddr,
		Inspect: subjects, Memory: mem, Jobs: jobs, Features: catalog, ConfigDir: cfgDir,
		Typed: typed, Config: cfg, ConfigPath: cfgPath, Models: pool.For, Build: buildProvider,
		BuildProvider: func(name string) (llm.Provider, error) {
			sub := *cfg
			sub.Provider = name
			return buildProvider(&sub)
		},
		Workspace: built.ws, CD: built.cd, MCP: mcpOpts, Skills: built.skillInjector,
		Doctor: func(ctx context.Context, w io.Writer) error {
			// The front-ends report; --fix is the command line's, so a
			// running session never changes files behind the user.
			return doctor(ctx, w, config.Overrides{Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Theme: cfg.Theme}, false)
		},
	})
}

// tuiMode opens the terminal session, which is what is left when no other
// mode applies.
func (r *root) tuiMode() error {
	if err := interactiveTerminal(); err != nil {
		return err
	}
	cfg, cwd, cfgDir, cfgPath := r.cfg, r.cwd, r.cfgDir, r.cfgPath
	ag, sess, mem, jobs, diag, subjects, catalog, typed, pool, built := r.ag, r.sess, r.mem, r.jobs, r.diag, r.subjects, r.catalog, r.typed, r.pool, r.built
	history, initial, needsConnect, deps := r.history, r.initial, r.needsConnect, r.deps
	ts, tokens := r.trust, r.tokens
	projectServers, projectHash, projectTrusted := r.projectServers, r.projectHash, r.projectTrusted
	hooksCfg, hooksHash, hooksTrusted := r.hooksCfg, r.hooksHash, r.hooksTrusted
	verifyCfg, verifyHash, verifyDefined, verifyTrusted := r.verifyCfg, r.verifyHash, r.verifyDefined, r.verifyTrusted
	projRules, permHash, permTrusted := r.projRules, r.permHash, r.permTrusted
	hintEngine, usage := r.hints, r.usage
	// Held on the root so /cd can re-point them at the directory it moved
	// to: each describes the project's own files, which the move re-reads.
	r.uiHooks = &tui.HookOptions{Global: cfg.Hooks, Project: hooksCfg, ProjectHash: hooksHash, ProjectTrusted: hooksTrusted, Trust: ts}
	r.uiVerify = &tui.VerifyOptions{Config: verifyCfg, ProjectHash: verifyHash, ProjectTrusted: verifyTrusted, Trust: ts}
	r.uiPerms = &tui.PermOptions{Project: projRules, ProjectHash: permHash, ProjectTrusted: permTrusted, Trust: ts,
		Save: func(kind string, r perm.Rule) error { return config.AddPermission(cfgPath, kind, r) }}
	return tui.Run(tui.Options{
		Plain: r.opts.plain || tui.PlainWanted(os.Getenv),
		Notes: cfg.Notes,
		Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd, ConfigDir: cfgDir,
		StatusLine: cfg.StatusLine,
		Workspace:  built.ws,
		CD:         built.cd,
		Memory:     mem, AutoMemory: cfg.AutoMemory(),
		Config: cfg, ConfigPath: cfgPath, Build: buildProvider, Connect: needsConnect,
		Resumed: len(history), Version: version, Theme: cfg.Theme, Themes: cfg.ThemeSet,
		InitialPrompt: initial,
		MCP: &tui.MCPOptions{
			Manager: &r.servers, Global: cfg.MCPServers,
			Project: projectServers, ProjectHash: projectHash, ProjectTrusted: projectTrusted, Trust: ts,
			Tokens: tokens,
		},
		Hooks:    r.uiHooks,
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
		Skills:        built.skillInjector,
		Models:        pool.For,
		Jobs:          jobs,
		Running:       deps.running,
		Typed:         typed,
		AutoTitle:     true,
		// The context is the session's: /doctor is cancelled when the
		// window closes. Threading it into doctor's own model call is
		// the command line's side of the change.
		Doctor: func(ctx context.Context, w io.Writer) error {
			// The front-ends report; --fix is the command line's, so a
			// running session never changes files behind the user.
			return doctor(ctx, w, config.Overrides{Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Theme: cfg.Theme}, false)
		},
		Verify:    r.uiVerify,
		Perms:     r.uiPerms,
		TrustGate: r.gate,
	})
}

// interactiveTerminal checks there is a terminal to draw the session on
// before starting one. With stdin piped the UI reads keys from /dev/tty;
// with no terminal at all (a CI job, `echo hi | janetcode` under cron)
// the library failed with "could not open a new TTY: open /dev/tty: no
// such device or address", which says nothing about -p.
func interactiveTerminal() error {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	f, err := os.Open("/dev/tty")
	if err != nil {
		return headless.BadInput("no terminal for the interactive session; for a run without one use janetcode -p \"<prompt>\" (piped input is added to the prompt)")
	}
	f.Close()
	return nil
}
