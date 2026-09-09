// Command harnessbench runs janetcode evals 01–20 in batches of five against
// several coding-agent CLIs pointed at one OpenAI-compatible endpoint.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harnessbench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("harnessbench", flag.ContinueOnError)
	evalsDir := fs.String("evals", "evals", "eval JSON directory")
	outDir := fs.String("out", "docs/benchmarks/harnesses", "directory for raw results and the report")
	scratch := fs.String("scratch", "", "directory for batch logs (optional)")
	janetBin := fs.String("janetcode", "bin/janetcode", "janetcode binary")
	model := fs.String("model", "qwen3.7-plus", "model id")
	baseURL := fs.String("base-url", "https://coding-intl.dashscope.aliyuncs.com/v1", "OpenAI-compatible base URL")
	harnessesDefault := strings.Join(eval.BenchHarnesses, ",")
	harnesses := fs.String("harnesses", harnessesDefault, "comma-separated harness names")
	timeout := fs.Duration("timeout", 3*time.Minute, "per-task wall timeout, applied to every harness")
	maxTurns := fs.Int("max-turns", 40, "model-call cap per task for every harness that has one (qwencode, hermes-agent, grok, janetcode; pi has no flag)")
	only := fs.String("only", "", "comma-separated task names to run (still grouped in batches of five)")
	filter := fs.String("filter", "01-20", "task set: 01-20, large, or all")
	reportOnly := fs.Bool("report-only", false, "rebuild REPORT.md from existing JSON files; do not call the API")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tasks, err := eval.Load(*evalsDir)
	if err != nil {
		return err
	}
	switch *filter {
	case "large":
		tasks = eval.FilterLarge(tasks)
		if *harnesses == harnessesDefault {
			*harnesses = strings.Join(eval.LargeHarnesses, ",")
		}
	case "all":
	default:
		tasks = eval.Filter01to20(tasks)
	}
	if *only != "" {
		tasks = eval.Filter(tasks, splitCSV(*only))
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks after filtering")
	}
	batches := eval.BatchesOfFive(tasks)

	key := strings.TrimSpace(firstEnv("DASHSCOPE_API_KEY", "OPENAI_API_KEY"))
	names := splitCSV(*harnesses)
	cfg := runConfig{
		Model:    *model,
		BaseURL:  strings.TrimRight(*baseURL, "/"),
		Key:      key,
		EvalsDir: *evalsDir,
		JanetBin: *janetBin,
		OutDir:   *outDir,
		Scratch:  *scratch,
		Timeout:  *timeout,
		MaxTurns: *maxTurns,
		RepoRoot: mustAbs("."),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	if cfg.Scratch != "" {
		if err := os.MkdirAll(cfg.Scratch, 0o755); err != nil {
			return err
		}
	}

	byHarness := map[string][]eval.BenchTaskResult{}
	notes := &strings.Builder{}
	fmt.Fprintf(notes, "# Harness invocation notes\n\n")
	fmt.Fprintf(notes, "Model `%s` at `%s`. Key supplied via process environment only.\n\n", cfg.Model, cfg.BaseURL)

	if !*reportOnly {
		for _, h := range names {
			fmt.Fprintf(os.Stderr, "\n== %s ==\n", h)
			results, note := runHarness(ctx, cfg, h, tasks, batches)
			results = redactResults(results, cfg.Key)
			path := filepath.Join(cfg.OutDir, h+".json")
			if prev, err := eval.ReadBenchJSON(path); err == nil {
				results = eval.MergeBenchResults(prev, results)
			}
			byHarness[h] = results
			if err := eval.WriteBenchJSON(path, results); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s (%d tasks)\n", path, len(results))
			fmt.Fprintf(notes, "## %s\n\n%s\n", h, strings.TrimSpace(note)+"\n")
		}
	}

	for _, h := range eval.BenchHarnesses {
		if _, ok := byHarness[h]; ok {
			continue
		}
		path := filepath.Join(cfg.OutDir, h+".json")
		rs, err := eval.ReadBenchJSON(path)
		if err != nil {
			continue
		}
		byHarness[h] = rs
	}
	if rs, ok := byHarness["hermes-agent"]; ok {
		byHarness["hermes-agent"] = backfillHermesUsage(rs)
		_ = eval.WriteBenchJSON(filepath.Join(cfg.OutDir, "hermes-agent.json"), byHarness["hermes-agent"])
	}

	meta := eval.ReportMeta{
		Model:    cfg.Model,
		Endpoint: cfg.BaseURL,
		When:     time.Now().UTC(),
		Note:     fmt.Sprintf("pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: %s wall budget for every harness and a fresh 2m for each check; %d model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)", cfg.Timeout, cfg.MaxTurns),
	}
	if *filter == "large" {
		meta.Tasks = "31–35 (five large evals, one batch of five); openclaw excluded"
		meta.Note += "; session input+cache-read may approach 1e6 tokens on a long tool loop"
	}
	report := eval.RenderComparativeReport(byHarness, meta)
	report = redact(report, cfg.Key)
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "REPORT.md"), []byte(report), 0o644); err != nil {
		return err
	}
	if !*reportOnly {
		if prev, err := os.ReadFile(filepath.Join(cfg.OutDir, "NOTES.md")); err == nil && len(byHarness) < len(eval.BenchHarnesses) {
			// Keep one previous layer only, or the file nests itself every run.
			prevText := string(prev)
			if i := strings.Index(prevText, "\n---\nPrevious notes preserved:"); i >= 0 {
				prevText = prevText[:i]
			}
			fmt.Fprintf(notes, "\n---\nPrevious notes preserved:\n\n%s\n", strings.TrimSpace(prevText))
		}
		if err := os.WriteFile(filepath.Join(cfg.OutDir, "NOTES.md"), []byte(redact(notes.String(), cfg.Key)), 0o644); err != nil {
			return err
		}
	}
	combined, _ := json.MarshalIndent(byHarness, "", "  ")
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "combined.json"), append([]byte(redact(string(combined), cfg.Key)), '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "wrote", filepath.Join(cfg.OutDir, "REPORT.md"))
	return nil
}

type runConfig struct {
	Model, BaseURL, Key string
	EvalsDir, JanetBin  string
	OutDir, Scratch     string
	Timeout             time.Duration
	MaxTurns            int
	RepoRoot            string
}

func runHarness(ctx context.Context, cfg runConfig, harness string, tasks []eval.Task, batches [][]eval.Task) ([]eval.BenchTaskResult, string) {
	switch harness {
	case "openclaw":
		return failOpenclaw(cfg, tasks)
	case "grok":
		defer cleanupGrokHome()
		if rs, note, ok := probeGrok(ctx, cfg, tasks); !ok {
			return rs, note
		}
	}

	needKey := harness != "openclaw"
	if needKey && cfg.Key == "" {
		reason := "DASHSCOPE_API_KEY/OPENAI_API_KEY not set in the process environment"
		return eval.UnavailableResults(harness, reason, "", "", tasks), reason + "\n"
	}

	var all []eval.BenchTaskResult
	var notes strings.Builder
	fmt.Fprintf(&notes, "Batches of five, up to five concurrent tasks. Timeout %s.\n", cfg.Timeout)
	for i, batch := range batches {
		n := i + 1
		logW, closeLog := batchLog(cfg, harness, n)
		fmt.Fprintf(logW, "harness=%s batch=%d tasks=", harness, n)
		for j, t := range batch {
			if j > 0 {
				fmt.Fprint(logW, ",")
			}
			fmt.Fprint(logW, t.Name)
		}
		fmt.Fprintln(logW)
		var rs []eval.BenchTaskResult
		if harness == "janetcode" {
			rs = runJanetBatch(ctx, cfg, n, batch, logW)
		} else {
			rs = runCLIBatch(ctx, cfg, harness, n, batch, logW)
		}
		closeLog()
		all = append(all, rs...)
		passed := 0
		for _, r := range rs {
			if r.Passed {
				passed++
			}
			fmt.Fprintf(os.Stderr, "  %s %s wall=%s in=%s cache=%s out=%s\n",
				mark(r.Passed, r.Unavailable), r.Task, time.Duration(r.WallTimeMS)*time.Millisecond, r.InputTokens, r.CacheReadTokens, r.OutputTokens)
		}
		fmt.Fprintf(&notes, "- batch %d: %d/%d passed\n", n, passed, len(rs))
	}
	return all, notes.String()
}

func batchLog(cfg runConfig, harness string, n int) (io.Writer, func()) {
	if cfg.Scratch == "" {
		return io.Discard, func() {}
	}
	path := filepath.Join(cfg.Scratch, fmt.Sprintf("%s-batch%d.log", harness, n))
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		return io.Discard, func() {}
	}
	return f, func() { _ = f.Close() }
}

func failOpenclaw(cfg runConfig, tasks []eval.Task) ([]eval.BenchTaskResult, string) {
	cmd, stdout, stderr, err := probeCmd(context.Background(), cfg, "openclaw", "--help")
	excerpt := strings.TrimSpace(stderr + "\n" + stdout)
	reason := "openclaw could not start"
	if err != nil {
		reason = "openclaw launch failed: " + err.Error()
	}
	if strings.Contains(excerpt, "Node.js") || strings.Contains(excerpt, "node") {
		reason = firstLine(excerpt)
	}
	note := fmt.Sprintf("Tried `%s`. Failure:\n\n```\n%s\n```\n", cmd, excerpt)
	return eval.UnavailableResults("openclaw", reason, cmd, excerpt, tasks), note
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func mark(ok, unavail bool) string {
	if unavail {
		return "N/A"
	}
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "***")
	}
	s = skSp.ReplaceAllString(s, "sk-sp-***")
	s = skOther.ReplaceAllString(s, "sk-***")
	return s
}

func backfillHermesUsage(rs []eval.BenchTaskResult) []eval.BenchTaskResult {
	for i := range rs {
		if rs[i].InputTokens.Set || rs[i].OutputTokens.Set {
			continue
		}
		u := eval.ParseUsageText(rs[i].StderrExcerpt)
		if u.HasTokens() {
			rs[i].ApplyUsage(u)
		}
	}
	return rs
}

func redactResults(rs []eval.BenchTaskResult, key string) []eval.BenchTaskResult {
	for i := range rs {
		rs[i].Reason = redact(rs[i].Reason, key)
		rs[i].Command = redact(rs[i].Command, key)
		rs[i].StderrExcerpt = redact(rs[i].StderrExcerpt, key)
		rs[i].UnavailableReason = redact(rs[i].UnavailableReason, key)
	}
	return rs
}
