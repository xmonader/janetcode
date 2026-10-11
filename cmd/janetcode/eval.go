package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/minimal"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/treesitter"
)

// promptRecord is what goes in the results file about the prompt: which
// file replaced the base, what was appended, and a digest of the base as
// it was read, so an experiment stays traceable after the file changes.
func promptRecord(o prompt.Override) *eval.PromptRecord {
	if o.Base == "" && o.Append == "" {
		return nil
	}
	r := &eval.PromptRecord{BaseFile: o.BaseFile, Append: o.Append}
	if o.Base != "" {
		sum := sha256.Sum256([]byte(o.Base))
		r.SHA256 = hex.EncodeToString(sum[:])
	}
	return r
}

type evalOptions struct {
	path, only, jsonOut string
	jobs, runs          int
	keep                bool
	timeout             time.Duration
	maxTurns            int
	noToolLimit         bool
	// strictProvenance fails the whole eval when any run was answered by
	// another vendor's model, instead of leaving those runs out.
	strictProvenance           bool
	compactAt                  int
	facts                      int
	factsBlind                 bool
	compare, off, route, label string
	// gate turns on the verification gate and the decision provider for
	// the eval agent (--eval-gate). Off by default so a run without it is
	// byte-identical to every run recorded before it existed.
	gate bool
	// prompt records a replaced or extended system prompt, so an A/B of
	// the prompt itself can be told apart in the results file.
	prompt prompt.Override
	// seq is --eval-sequential: the gate whose always-valid verdict
	// against the baseline may end the run early ("" runs every round).
	seq string
}

// evalVariant is the label a results file records for a run: --eval-label
// when given, otherwise "off:<behaviour>" and/or "route:<model>" for
// whichever of --eval-off / --eval-route (EVAL_OFF / EVAL_ROUTE) were set
// — comma-separated when both were, so a run that both ablates something
// and routes can still be told apart from one that only did either. This
// is the naming EVAL_OFF has always used, extended to cover EVAL_ROUTE
// the same way (see docs/manual/evals.md, "Measuring step routing").
func evalVariant(label, off, route string) string {
	if label != "" {
		return label
	}
	var parts []string
	if strings.TrimSpace(off) != "" {
		parts = append(parts, "off:"+off)
	}
	if route != "" {
		parts = append(parts, "route:"+route)
	}
	return strings.Join(parts, ",")
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
	off, err := agent.ParseSwitches(o.off)
	if err != nil {
		return fmt.Errorf("--eval-off: %w", err)
	}
	variant := evalVariant(o.label, o.off, o.route)
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
	// A pool, not a single provider, because route_cheap (o.route) may
	// name a model on a different backend than cfg's own; it resolves
	// through the same config.RouteFor a real session's provider pool
	// uses (see cmd/janetcode/provider.go), so an eval run measures the
	// feature exactly as a session would use it.
	pool := newProviderPool(cfg, prov)
	r := &eval.Runner{Jobs: o.jobs, Runs: runs, Keep: o.keep, Log: os.Stderr, Timeout: o.timeout, IgnoreToolLimit: o.noToolLimit, Facts: o.facts, FactsBlind: o.factsBlind, NewAgent: func(root, hiddenGate string) (*agent.Agent, error) {
		// The eval agent carries the tools a real session does, minus the
		// ones that need a terminal or the network. A benchmark run
		// against a smaller agent measures something nobody uses.
		ag, err := minimal.New(root, cfg, llm.ForTaskOr(prov), off, o.prompt, filepath.Join(archiveDir, filepath.Base(root)), treesitter.Parser())
		if err != nil {
			return nil, err
		}
		ag.MaxIterations = maxIter
		ag.CompactAt = compactAt
		ag.Route = pool.For
		ag.RouteCheap = o.route
		if o.gate {
			wireEvalGate(ag, root, cfg, off, hiddenGate)
		}
		evalToolsOnce.Do(func() { evalToolNames = ag.Tools.Names() })
		return ag, nil
	}}
	results, err := seqRun(ctx, r, tasks, baseline, o.seq)
	if err != nil {
		return err
	}
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
		eval.WriteSequential(os.Stdout, eval.CompareSequential(baseline.Results, results, 0.05))
	}
	if o.jsonOut != "" {
		if err := eval.WriteFile(o.jsonOut, eval.File{Model: cfg.Provider + "/" + cfg.Model, Variant: variant, Results: results, Prompt: promptRecord(o.prompt)}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", o.jsonOut)
	}
	return evalVerdict(eval.Summarize(results), len(results), passed, o.strictProvenance)
}

// evalVerdict is the eval's exit: an error when a scored run failed, when
// no run reached the requested model at all (a set that every run of
// which was lost to the gateway has not passed, it has not been run), or
// — with strict provenance — when any run was answered by another
// vendor's model. Runs lost to the gateway or answered by another model
// are otherwise left out, not failed.
func evalVerdict(s eval.Summary, runs, passed int, strict bool) error {
	switch {
	case strict && s.Mismatched > 0:
		return fmt.Errorf("%d of %d eval runs were answered by another vendor's model than the one requested (--eval-strict-provenance)", s.Mismatched, runs)
	case runs > 0 && s.Total == 0:
		return fmt.Errorf("none of %d eval runs reached the requested model (%d lost to the provider, %d answered by another vendor's model)", runs, s.Infra, s.Mismatched)
	case passed < s.Total:
		return fmt.Errorf("%d of %d scored eval tasks failed", s.Total-passed, s.Total)
	}
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
	fmt.Fprintf(os.Stdout, "%s — %s, %s\n\n", label, textfmt.Plural(len(f.Results), "result"), f.Model)
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
	eval.WriteSequential(os.Stdout, eval.CompareSequential(b.Results, f.Results, 0.05))
	return nil
}

// evalToolNames is what the eval agent was given, so the adoption report
// can name the tools nothing ever called.
var (
	evalToolNames []string
	evalToolsOnce sync.Once
)

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
		return fmt.Errorf("no runs with trajectory features in %s; results written before features were recorded cannot be used", textfmt.Plural(files, "result file"))
	}
	p, err := agent.Train(samples)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "trajectory critic trained on %d runs (%d passed, %d failed) from %s\n", p.Samples, p.Positives, p.Negatives, textfmt.Plural(files, "file"))
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
