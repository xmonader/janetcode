package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rates"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/worktree"
)

// The steps between the command line and the composition root: the
// configuration, the provider, the worktree the session runs in, and the
// modes that need those but no agent.

// loadConfig reads the configuration and applies the flags that are
// settings rather than eval plumbing. needsConnect is the interactive
// first run: no credentials yet, but a terminal or a browser to ask in,
// so the TUI or the page opens on /connect instead of the process failing.
func loadConfig(o *options) (cfg *config.Config, needsConnect bool, err error) {
	cfg, err = config.Load(config.Overrides{Provider: o.provider, Model: o.model, Effort: o.effort, Theme: o.themeFlag, AutoApprove: o.yolo})
	// --web asks in the browser, so it needs no terminal to ask in.
	needsConnect = err != nil && errors.Is(err, config.ErrNoCredentials) && !o.headless() && (o.web || term.IsTerminal(int(os.Stdin.Fd())))
	// A recipe replays tool calls with no model call; with no key it runs
	// and, on a failed step, exits instead of handing over (recipe.go).
	// It used to refuse to start for want of a key it never needed.
	if err != nil && errors.Is(err, config.ErrNoCredentials) && o.recipeName != "" && o.promptText == "" {
		needsConnect = true
	}
	// The turn cap and the compaction threshold are settings, not eval
	// plumbing: a headless run driven by a benchmark needs the same two
	// knobs the eval runner takes, and applying them to the config means
	// every path downstream -- headless, TUI and ACP -- reads one value.
	// config.Load returns no configuration at all for a file it cannot
	// use; only the missing-credentials case has one to adjust.
	if cfg != nil && o.safeMode {
		applySafeMode(cfg)
	}
	if cfg != nil {
		if o.maxTurns > 0 {
			cfg.MaxIterations = o.maxTurns
		}
		if o.compactAt > 0 {
			cfg.CompactAt = o.compactAt
		}
		// --set is /config for a run with nobody to type in it: the same
		// catalogue, the same parsing and the same validation, applied to
		// the loaded config and never written back to the file.
		for _, kv := range o.sets {
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				return cfg, needsConnect, headless.BadInput("--set wants key=value, got %q", kv)
			}
			if _, e := config.ApplyKV(cfg, strings.TrimSpace(key), strings.TrimSpace(val)); e != nil {
				return cfg, needsConnect, headless.BadInput("--set %s: %v (janetcode --inspect config lists the keys)", key, e)
			}
		}
		// A decisions block reached through --set is validated the same
		// way the file's is, so a run cannot start on one the next load
		// would refuse.
		if e := cfg.Decisions.Validate(); e != nil {
			return cfg, needsConnect, e
		}
	}
	return cfg, needsConnect, err
}

// startRates points the configuration at the cached price table and, when
// the cache is stale, refreshes it in the background. The returned func
// stops that refresh and waits for it.
//
// The refresh writes model-rates.json into the config directory, and it is
// background work only for as long as the run lasts: a goroutine still
// downloading 2.3 MB after run returns outlives whatever set that
// directory up. A test's TempDir cleanup raced exactly that and failed
// with ".config/janetcode not empty". Cancelling and waiting on the way
// out means nothing writes there after the run.
func startRates(cfg *config.Config) func() {
	cfgPath, err := config.Path()
	if err != nil {
		return func() {}
	}
	dir := filepath.Dir(cfgPath)
	cfg.RatesTable = rates.LoadLazy(dir)
	if rates.Fresh(dir) {
		return func() {}
	}
	rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rates.Refresh(rctx, dir)
	}()
	return func() {
		cancel()
		<-done
	}
}

// offlineInspect answers --inspect when there are no credentials.
// Inspecting the configuration is what you reach for when the
// configuration is the problem, so it must not need a working one. The
// subjects that describe a running session are unavailable, and the
// listing says which those are.
func offlineInspect(fs *flag.FlagSet, o *options, cfg *config.Config, cerr error) (bool, error) {
	if o.inspectWhat == "" || cerr == nil || !errors.Is(cerr, config.ErrNoCredentials) {
		return false, nil
	}
	r := inspect.New()
	cfgPath, _ := config.Path()
	cwd, _ := os.Getwd()
	registerOfflineSubjects(r, cfg, cfgPath, cwd, cerr)
	registerFeatureSubject(r, buildCatalog(fs, nil))
	registerThemeSubject(r, cfg)
	return true, inspectOnce(r, o.inspectWhat, os.Stdout, true)
}

// openWorktree moves the run into a git worktree on --worktree's branch.
// Everything downstream is rooted at the working directory -- tools, the
// repository map, language servers, hooks and the session's own record --
// so the process changes into it. The returned func reports the branch on
// the way out.
func openWorktree(o *options, cwd string) (string, func(), error) {
	if o.wtBranch == "" {
		return cwd, func() {}, nil
	}
	wt, err := worktree.Open(context.Background(), cwd, o.wtBranch)
	if err != nil {
		return cwd, func() {}, err
	}
	verb := "reusing"
	if wt.Created {
		verb = "created"
	}
	fmt.Fprintf(os.Stderr, "worktree: %s %s on branch %s\n", verb, wt.Path, wt.Branch)
	report := func() {
		fmt.Fprintf(os.Stderr, "worktree: branch %s has %s (%s)\nmerge it with: git merge %s\n", wt.Branch, wt.Summarize(context.Background()), wt.Path, wt.Branch)
	}
	if err := os.Chdir(wt.Path); err != nil {
		return cwd, report, err
	}
	return wt.Path, report, nil
}

// whatIfMode runs --what-if: a counterfactual replay in an isolated
// worktree, whose live part needs a provider.
func whatIfMode(o *options, cfg *config.Config, prov llm.Provider) (bool, error) {
	if o.whatIf == "" {
		return false, nil
	}
	if prov == nil {
		return true, errors.New("--what-if needs a configured provider for the live part")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return true, runWhatIf(ctx, cfg, prov, whatIfOptions{
		session: o.whatIf, fromTurn: o.whatIfFrom, off: o.whatIfOff, deny: o.deny, prompt: o.whatIfPrompt,
		keep: o.whatIfKeep, json: o.jsonOut,
	})
}

// evalMode runs --eval against the configured provider.
func evalMode(o *options, cfg *config.Config, prov llm.Provider) (bool, error) {
	if o.evalPath == "" {
		return false, nil
	}
	if prov == nil {
		return true, errors.New("--eval needs a configured provider")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ov, err := prompt.LoadOverride(os.Getenv, o.appendSystem)
	if err != nil {
		return true, err
	}
	return true, runEval(ctx, cfg, prov, evalOptions{path: o.evalPath, jobs: o.evalJobs, only: o.evalOnly, jsonOut: o.evalJSON, keep: o.evalKeep,
		runs: o.evalRuns, compare: o.evalCompare, off: o.evalOff, route: o.evalRoute, label: o.evalLabel, prompt: ov,
		timeout: o.evalTimeout, maxTurns: o.evalTurns, noToolLimit: o.evalNoLimit, compactAt: o.evalCompact, facts: o.evalFacts, factsBlind: o.evalBlind, gate: o.evalGate})
}

// openSession resumes the session --resume or --continue names, or starts
// a new one for this directory.
func openSession(o *options, cfg *config.Config, sessDir, cwd string) (*session.Session, []llm.Message, error) {
	resumeID := o.resume
	if o.cont && resumeID == "" {
		var err error
		if resumeID, err = session.Latest(sessDir, cwd); err != nil {
			return nil, nil, err
		}
	}
	if resumeID != "" {
		sess, history, err := session.Open(sessDir, resumeID)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, headless.BadInput("no session %q (janetcode --sessions lists this directory's)", resumeID)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("resume: %w", err)
		}
		return sess, history, nil
	}
	sess, err := session.New(sessDir, cwd, cfg.Provider, cfg.Model)
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil, nil
}

// maxBackgroundStdin is the most piped input --bg will fold into the
// prompt. It goes into the child's argv, and every platform caps that:
// 128 KB is well inside the smallest of those limits and larger than any
// diff or log anybody pipes at a review.
const maxBackgroundStdin = 128 * 1024

// bgPrompt is the prompt the background child is launched with. The
// child's stdin is closed, so anything piped into the parent has to be
// carried in the prompt -- in the same <stdin> wrapper a foreground -p
// run uses, so the model sees the same thing either way. Without this,
// `git diff | janetcode --bg -p review` reviewed nothing and said so
// nowhere. Too much to fit in a command line is refused rather than
// silently cut: a review of the first half of a diff is worse than an
// error saying which flag to drop.
func bgPrompt(text, piped string) (string, error) {
	if piped == "" {
		return text, nil
	}
	if len(piped) > maxBackgroundStdin {
		return "", fmt.Errorf("%w: --bg carries piped input in the background process's command line, and %d bytes is too much for that; run without --bg, or write it to a file and name it with @path",
			headless.ErrBadInput, len(piped))
	}
	return text + "\n\n<stdin>\n" + piped + "\n</stdin>", nil
}

// bgMode detaches the run: --bg starts the prompt in a background
// process, prints the session id and returns.
func bgMode(o *options, sess *session.Session, initial string) (bool, error) {
	if !o.bg {
		return false, nil
	}
	text := strings.TrimSpace(o.promptText)
	if text == "" {
		text = initial
	}
	if text == "" {
		return true, fmt.Errorf("%w: --bg needs a prompt (-p or an argument)", headless.ErrBadInput)
	}
	text, err := bgPrompt(text, pipedStdin())
	if err != nil {
		return true, err
	}
	return true, launchBackground(sess, text, o.addDir, append(bgPermissionArgs(o), bgRunArgs(o)...))
}
