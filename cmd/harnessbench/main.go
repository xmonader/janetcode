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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	claudeModel := fs.String("claude-model", "", "model for the claudecode harness (haiku, sonnet, opus); it uses its own account")
	timeout := fs.Duration("timeout", 3*time.Minute, "per-task wall timeout, applied to every harness")
	maxTurns := fs.Int("max-turns", 40, "model-call cap per task for every harness that has one (qwencode, hermes-agent, grok, janetcode; pi has no flag)")
	only := fs.String("only", "", "comma-separated task names to run (still grouped in batches of five)")
	filter := fs.String("filter", "01-20", "task set: 01-20, large, session, or all")
	reportOnly := fs.Bool("report-only", false, "rebuild REPORT.md from existing JSON files; do not call the API")
	meterLog := fs.String("meter-log", "", "modelproxy -meter JSONL log to cross-check harness-reported usage against (optional)")
	janetDriver := fs.String("janetcode-driver", "eval", "how to run janetcode: eval (its own eval harness, a batch per process) or headless (one `-p --output-format json` per task, driven exactly like the other CLIs)")
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
	case "claude":
		// janetcode against Claude Code on the session set.
		tasks = eval.Filter(tasks, []string{"session"})
		if *harnesses == harnessesDefault {
			*harnesses = strings.Join(eval.ClaudeHarnesses, ",")
		}
	case "session":
		// Multi-prompt sessions: the context-management comparison. Each
		// prompt is one invocation continuing the harness's session.
		tasks = eval.Filter(tasks, []string{"session"})
		if *harnesses == harnessesDefault {
			*harnesses = strings.Join(eval.SessionHarnesses, ",")
		}
	case "all":
	default:
		tasks = eval.Filter01to20(tasks)
	}
	if *only != "" {
		tasks = eval.Filter(tasks, textfmt.SplitCSV(*only))
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks after filtering")
	}
	batches := eval.BatchesOfFive(tasks)

	// The key must match the endpoint. Preferring one vendor's variable
	// over another sends the wrong key when both are set, which arrives
	// as a 401 that looks like the model rejecting the work.
	key := strings.TrimSpace(keyFor(*baseURL))
	names := textfmt.SplitCSV(*harnesses)
	cfg := runConfig{
		Model:       *model,
		BaseURL:     strings.TrimRight(*baseURL, "/"),
		Key:         key,
		EvalsDir:    *evalsDir,
		JanetBin:    *janetBin,
		OutDir:      *outDir,
		Scratch:     *scratch,
		ClaudeModel: *claudeModel,
		Timeout:     *timeout,
		MaxTurns:    *maxTurns,
		RepoRoot:    mustAbs("."),
		JanetDriver: *janetDriver,
		MeterLog:    *meterLog,
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
	// ClaudeModel is what the claudecode harness runs on; it uses its own
	// account rather than the endpoint above.
	ClaudeModel string
	Timeout     time.Duration
	MaxTurns    int
	RepoRoot    string
	// JanetDriver is how janetcode is run: "eval" drives its own eval
	// harness (one process per batch, its results file read back), and
	// "headless" drives it like every other CLI, one `-p` per task, and
	// reads the result object it now prints.
	JanetDriver string
	// MeterLog is a modelproxy -meter JSONL log used to cross-check each
	// task's harness-reported usage against what the provider billed;
	// empty disables the check.
	MeterLog string
}

func runHarness(ctx context.Context, cfg runConfig, harness string, tasks []eval.Task, batches [][]eval.Task) ([]eval.BenchTaskResult, string) {
	switch harness {
	case "openclaw":
		if rs, note, ok := prepareOpenclaw(ctx, cfg, tasks); !ok {
			return rs, note
		}
	case "claudecode":
		if _, err := exec.LookPath("claude"); err != nil {
			return eval.UnavailableResults("claudecode", "claude is not installed", "claude -p", err.Error(), tasks), "Not installed.\n"
		}
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
		if ctx.Err() != nil {
			// A ctrl-c reaches every child at once, so the remaining
			// batches would all come back as instant failures — and the
			// caller merges them over the JSON of the last good run,
			// replacing real rows with "timed out". Stop with what ran.
			fmt.Fprintf(&notes, "- interrupted after batch %d; the rest were not run\n", i)
			break
		}
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
		if harness == "janetcode" && cfg.JanetDriver != "headless" {
			rs = runJanetBatch(ctx, cfg, n, batch, logW)
		} else {
			rs = runCLIBatch(ctx, cfg, harness, n, batch, logW)
		}
		closeLog()
		if ctx.Err() != nil {
			// The interrupt landed while this batch ran: its tasks were
			// killed part-way, and their rows say so rather than how the
			// harness did — the same rows the check above keeps out.
			fmt.Fprintf(&notes, "- batch %d was interrupted and its results discarded\n", n)
			break
		}
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

// openclawProfile isolates this benchmark's openclaw state, so a run
// neither disturbs the operator's own gateway nor is blocked by it:
// openclaw refuses --local while a Gateway holds the same state directory.
const openclawProfile = "janetbench"

// prepareOpenclaw configures a private openclaw profile to reach the
// endpoint under test, and reports why it cannot when it cannot.
//
// Four separate things stop openclaw being benchmarked out of the box, and
// each was found the hard way: it needs a Node newer than the one a
// bundled runtime may put first on PATH; it refuses --local while a
// Gateway is running for the same state directory; its provider catalogue
// has to be told about a custom endpoint before it will accept the model
// name; and it works in its own workspace rather than the process's
// working directory, so tasks run somewhere else entirely unless
// agents.defaults.workspace is pointed at them.
func prepareOpenclaw(ctx context.Context, cfg runConfig, tasks []eval.Task) ([]eval.BenchTaskResult, string, bool) {
	if _, err := exec.LookPath("openclaw"); err != nil {
		return eval.UnavailableResults("openclaw", "openclaw is not installed", "openclaw", err.Error(), tasks), "Not installed.\n", false
	}
	if cfg.Key == "" {
		return eval.UnavailableResults("openclaw", "no API key for the endpoint under test", "openclaw", "", tasks), "No key.\n", false
	}
	patch := map[string]any{
		"models": map[string]any{
			"mode": "merge",
			"providers": map[string]any{
				"bench": map[string]any{
					"baseUrl": cfg.BaseURL,
					"apiKey":  cfg.Key,
					"api":     "openai-completions",
					"models": []any{map[string]any{
						"id": cfg.Model, "name": cfg.Model,
						"contextWindow": 128000, "maxTokens": 8192,
					}},
				},
			},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return eval.UnavailableResults("openclaw", "config patch: "+err.Error(), "openclaw config patch", "", tasks), "", false
	}
	f, err := os.CreateTemp("", "openclaw-patch-*.json")
	if err != nil {
		return eval.UnavailableResults("openclaw", "config patch: "+err.Error(), "openclaw config patch", "", tasks), "", false
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return eval.UnavailableResults("openclaw", "config patch: "+err.Error(), "openclaw config patch", "", tasks), "", false
	}
	f.Close()
	// The base profile keeps the model list of the last endpoint this
	// benchmark ran; a patch that would drop an entry from it is refused
	// unless the list is named as one to replace, and openclaw then never
	// launches for the second model tried.
	cmd := exec.CommandContext(ctx, "openclaw", "--profile", openclawProfile, "config", "patch", "--replace-path", "models.providers.bench.models", "--file", f.Name())
	cmd.Env = openclawEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		excerpt := strings.TrimSpace(string(out))
		return eval.UnavailableResults("openclaw", "openclaw config patch failed: "+textfmt.FirstLine(strings.TrimSpace(excerpt), 0), "openclaw config patch", excerpt, tasks),
			fmt.Sprintf("Configuring openclaw failed:\n\n```\n%s\n```\n", excerpt), false
	}
	return nil, "", true
}

// openclawEnv puts a Node that satisfies openclaw first on PATH. A
// bundled runtime shipped with another tool can be older than its
// minimum, and openclaw then refuses to start at all.
func openclawEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			kv = "PATH=/usr/bin:/usr/local/bin:" + strings.TrimPrefix(kv, "PATH=")
		}
		out = append(out, kv)
	}
	return out
}

// keyFor picks the credential that belongs to the endpoint, falling back
// to whichever is set when the host is not one it knows.
func keyFor(baseURL string) string {
	host := strings.ToLower(baseURL)
	switch {
	case strings.Contains(host, "openrouter"):
		if k := os.Getenv("OPENROUTER_API_KEY"); k != "" {
			return k
		}
	case strings.Contains(host, "dashscope"):
		if k := os.Getenv("DASHSCOPE_API_KEY"); k != "" {
			return k
		}
	case strings.Contains(host, "deepseek.com"):
		if k := os.Getenv("DEEPSEEK_API_KEY"); k != "" {
			return k
		}
	case strings.Contains(host, "api.anthropic.com"):
		if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
			return k
		}
	}
	return firstEnv("OPENAI_API_KEY", "OPENROUTER_API_KEY", "DASHSCOPE_API_KEY", "ANTHROPIC_API_KEY")
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
