// effbench compares coding agents on janetcode's eval tasks through one
// metering proxy. See docs/manual/evals.md ("Cross-agent benchmark").
//
//	effbench run     --model M --arms janetcode,aider [--dry-run]
//	effbench report  DIR [--ref-arm janetcode] [--ref-tps N] [-o FILE]
//	effbench selftest
//
// Live runs read AIBROKER_AI02_API_KEY (and optionally AIBROKER_AI02_BASE_URL)
// and go to the ai02 broker; OpenRouter is refused.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/effbench"
	"github.com/xmonader/janetcode/internal/eval"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "effbench:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: effbench run|report|selftest [flags]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch args[0] {
	case "run":
		return runCmd(ctx, args[1:], stdout)
	case "report":
		return reportCmd(args[1:], stdout)
	case "selftest":
		fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
		out := fs.String("out", "evals/effbench-selftest", "where the selftest writes its runs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return effbench.SelfTest(ctx, *out, stdout)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func runCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	campaign := fs.String("campaign", time.Now().Format("20060102-1504"), "campaign name")
	model := fs.String("model", "", "model id served by the broker (required)")
	arms := fs.String("arms", "janetcode", "comma-separated adapters")
	seeds := fs.Int("seeds", 1, "runs per task and adapter")
	evals := fs.String("evals", "evals", "eval task directory")
	only := fs.String("only", "", "comma-separated words selecting tasks by name")
	out := fs.String("out", "evals/effbench", "output directory")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-run timeout")
	adapters := fs.String("adapters", "", "JSON file of extra or overriding adapters")
	janet := fs.String("janetcode", "", "janetcode binary for the janetcode adapter (default: PATH)")
	dry := fs.Bool("dry-run", false, "print the plan and adapter availability; no proxy, no network")
	keep := fs.Bool("keep-ws", false, "keep each run's workspace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("--model is required")
	}
	tasks, err := eval.Load(*evals)
	if err != nil {
		return err
	}
	if *only != "" {
		tasks = eval.Filter(tasks, strings.Split(*only, ","))
	}
	all, err := effbench.LoadAdapters(*adapters)
	if err != nil {
		return err
	}
	var picked []effbench.Adapter
	for _, name := range strings.Split(*arms, ",") {
		name = strings.TrimSpace(name)
		found := false
		for _, a := range all {
			if a.Name == name {
				picked, found = append(picked, a), true
			}
		}
		if !found {
			return fmt.Errorf("unknown adapter %q", name)
		}
	}
	cfg := &effbench.Config{Out: *out, Campaign: *campaign, Model: *model, Seeds: *seeds, Timeout: *timeout,
		Adapters: picked, Tasks: tasks, JanetBin: *janet, DryRun: *dry, KeepWS: *keep,
		Logf: func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) }}
	if *dry {
		for _, a := range picked {
			p, ok := a.Available()
			fmt.Fprintf(stdout, "adapter %-10s available=%v %s\n", a.Name, ok, p)
		}
		_, err := cfg.Execute(ctx)
		return err
	}
	cfg.Upstream, cfg.Key, err = effbench.UpstreamFromEnv()
	if err != nil {
		return err
	}
	if _, err := cfg.Execute(ctx); err != nil {
		return err
	}
	return reportCmd([]string{"-o", *out + "/" + *campaign + "/report.md", *out + "/" + *campaign}, stdout)
}

func reportCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	refArm := fs.String("ref-arm", "janetcode", "adapter whose decode speed is the common speed")
	refTPS := fs.Float64("ref-tps", 0, "fixed common decode speed in tok/s (overrides --ref-arm)")
	outFile := fs.String("o", "", "also write the report to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: effbench report DIR [-o FILE]")
	}
	runs, err := effbench.LoadRuns(fs.Arg(0))
	if err != nil {
		return err
	}
	w := stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			return err
		}
		defer f.Close()
		w = io.MultiWriter(stdout, f)
	}
	effbench.WriteReport(w, runs, *refArm, *refTPS)
	return nil
}
