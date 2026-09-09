package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/tool"
)

var (
	skSp    = regexp.MustCompile(`sk-sp-\S+`)
	skOther = regexp.MustCompile(`sk-[A-Za-z0-9.*]{8,}`)
)

func runJanetBatch(ctx context.Context, cfg runConfig, batchN int, tasks []eval.Task, logW io.Writer) []eval.BenchTaskResult {
	bin := cfg.JanetBin
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(cfg.RepoRoot, bin)
	}
	if _, err := os.Stat(bin); err != nil {
		reason := "janetcode binary not found at " + bin
		return eval.UnavailableResults("janetcode", reason, bin+" --eval", err.Error(), tasks)
	}
	names := make([]string, len(tasks))
	for i, t := range tasks {
		names[i] = t.Name
	}
	jsonOut := filepath.Join(os.TempDir(), fmt.Sprintf("janetcode-batch%d-%d.json", batchN, time.Now().UnixNano()))
	xdg, _ := os.MkdirTemp("", "janetcode-xdg-")
	defer os.RemoveAll(xdg)

	args := []string{
		"--eval", cfg.EvalsDir,
		"--eval-only", strings.Join(names, ","),
		"--eval-jobs", fmt.Sprintf("%d", len(tasks)),
		"--eval-json", jsonOut,
		"--provider", "openai",
		"--model", cfg.Model,
		"--eval-timeout", cfg.Timeout.String(),
		"--eval-max-turns", fmt.Sprint(cfg.MaxTurns),
		"--eval-no-tool-limit",
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	tool.Supervise(cmd)
	cmd.Dir = cfg.RepoRoot
	cmd.Env = childEnv(cfg, map[string]string{
		"XDG_CONFIG_HOME":    xdg,
		"OPENAI_API_KEY":     cfg.Key,
		"OPENAI_BASE_URL":    cfg.BaseURL,
		"DASHSCOPE_API_KEY":  cfg.Key,
		"JANETCODE_PROVIDER": "openai",
		"JANETCODE_MODEL":    cfg.Model,
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, logW)
	cmd.Stderr = io.MultiWriter(&stderr, logW)
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	fmt.Fprintf(logW, "janetcode batch %d exit err=%v wall=%s\n", batchN, err, wall)

	f, rerr := eval.ReadFile(jsonOut)
	_ = os.Remove(jsonOut)
	if rerr != nil {
		excerpt := stderr.String() + "\n" + stdout.String()
		reason := "janetcode did not write eval JSON: " + rerr.Error()
		if err != nil {
			reason += "; " + err.Error()
		}
		rs := eval.UnavailableResults("janetcode", reason, strings.Join(append([]string{bin}, args...), " "), excerpt, tasks)
		for i := range rs {
			rs[i].Unavailable = false
			rs[i].SetWall(wall / time.Duration(len(tasks)))
			rs[i].ExitCode = exitCode(err)
		}
		return rs
	}
	byName := map[string]eval.Result{}
	for _, r := range f.Results {
		byName[r.Name] = r
	}
	out := make([]eval.BenchTaskResult, 0, len(tasks))
	for _, t := range tasks {
		r, ok := byName[t.Name]
		if !ok {
			br := eval.BenchTaskResult{Harness: "janetcode", Task: t.Name, Batch: batchN, Reason: "missing from eval JSON"}
			br.SetWall(0)
			out = append(out, br)
			continue
		}
		br := eval.FromJanetResult("janetcode", batchN, r)
		br.Command = "janetcode --eval --eval-only " + t.Name + " --provider openai --model " + cfg.Model
		if err != nil && !br.Passed {
			br.StderrExcerpt = trimCap(stderr.String(), 1500)
			br.ExitCode = exitCode(err)
		}
		out = append(out, br)
	}
	return out
}

func runCLIBatch(ctx context.Context, cfg runConfig, harness string, batchN int, tasks []eval.Task, logW io.Writer) []eval.BenchTaskResult {
	var mu sync.Mutex
	results := make([]eval.BenchTaskResult, len(tasks))
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t eval.Task) {
			defer wg.Done()
			r := runOne(ctx, cfg, harness, batchN, t, &mu, logW)
			results[i] = r
		}(i, t)
	}
	wg.Wait()
	return results
}

func runOne(ctx context.Context, cfg runConfig, harness string, batchN int, t eval.Task, mu *sync.Mutex, logW io.Writer) eval.BenchTaskResult {
	res := eval.BenchTaskResult{Harness: harness, Task: t.Name, Batch: batchN}
	tctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "harness-"+harness+"-"+evalSanitize(t.Name)+"-")
	if err != nil {
		res.Reason = "mkdir: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	if err := seedWorkdir(t, dir); err != nil {
		res.Reason = "setup: " + err.Error()
		return res
	}
	start := time.Now()
	stdout, stderr, argv, code, runErr := invoke(tctx, cfg, harness, t, dir)
	res.SetWall(time.Since(start))
	res.Command = redact(strings.Join(argv, " "), cfg.Key)
	res.ExitCode = code
	if cfg.Scratch != "" {
		logp := filepath.Join(cfg.Scratch, harness+"-"+evalSanitize(t.Name)+".log")
		_ = os.WriteFile(logp, []byte(redact(stdout+"\n--- stderr ---\n"+stderr, cfg.Key)), 0o644)
	}
	res.StderrExcerpt = trimCap(stderr, 1500)

	text := eval.ExtractAssistantText([]byte(stdout))
	if text == "" {
		text = strings.TrimSpace(stdout)
	}
	outPath := filepath.Join(dir, ".eval-output.txt")
	_ = os.WriteFile(outPath, []byte(text+"\n"), 0o644)

	u := eval.ParseUsageJSON([]byte(stdout))
	u2 := eval.ParseUsageText(stdout + "\n" + stderr)
	u = fillUsage(u, u2)
	res.ApplyUsage(u)

	if runErr != nil && tctx.Err() != nil {
		res.Reason = fmt.Sprintf("timed out after %s: %v", cfg.Timeout, runErr)
	} else if passed, why, cout := runCheck(ctx, t, dir, outPath); passed { // fresh budget: the check is not the harness's time
		res.Passed = true
		if why != "" {
			res.Reason = why
		}
		_ = cout
	} else {
		res.Reason = why
		if res.StderrExcerpt == "" {
			res.StderrExcerpt = trimCap(cout, 1500)
		}
	}

	mu.Lock()
	fmt.Fprintf(logW, "task %s passed=%v wall=%s in=%s cache=%s out=%s reason=%s\n",
		t.Name, res.Passed, res.WallTime, res.InputTokens, res.CacheReadTokens, res.OutputTokens, firstLine(res.Reason))
	mu.Unlock()
	return res
}

func fillUsage(a, b eval.UsageMetrics) eval.UsageMetrics {
	if !a.Input.Set {
		a.Input = b.Input
	}
	if !a.Output.Set {
		a.Output = b.Output
	}
	if !a.CacheRead.Set {
		a.CacheRead = b.CacheRead
	}
	if !a.CacheWrite.Set {
		a.CacheWrite = b.CacheWrite
	}
	if !a.ToolCalls.Set {
		a.ToolCalls = b.ToolCalls
	}
	if !a.Turns.Set {
		a.Turns = b.Turns
	}
	if !a.CostUSD.Set {
		a.CostUSD = b.CostUSD
	}
	return a
}

func seedWorkdir(t eval.Task, dir string) error {
	for name, content := range t.Files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath("git"); err == nil {
		_ = exec.Command("git", "-C", dir, "init", "-q").Run()
	}
	return nil
}

// runCheck runs the task's check with its own two-minute budget, like
// the native eval runner, so a harness that used its whole wall budget
// is not failed by a check that never got to run.
func runCheck(ctx context.Context, t eval.Task, dir, outPath string) (bool, string, string) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", t.Check)
	tool.Supervise(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EVAL_OUTPUT="+outPath, "EVAL_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := "check failed: " + err.Error()
		if s := strings.TrimSpace(string(out)); s != "" {
			msg += ": " + s
		}
		return false, msg, string(out)
	}
	return true, "", string(out)
}

func invoke(ctx context.Context, cfg runConfig, harness string, t eval.Task, dir string) (stdout, stderr string, argv []string, code int, err error) {
	var cmd *exec.Cmd
	extraEnv := map[string]string{
		"OPENAI_API_KEY":    cfg.Key,
		"OPENAI_BASE_URL":   cfg.BaseURL,
		"DASHSCOPE_API_KEY": cfg.Key,
	}
	switch harness {
	case "pi":
		argv = []string{"pi", "-p", "--mode", "json", "--provider", "dashscope", "--model", cfg.Model, "--no-session", t.Prompt}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	case "qwencode":
		argv = []string{"qwen", "--yolo", "--output-format", "json", "--auth-type", "openai",
			"--openai-base-url", cfg.BaseURL, "--model", cfg.Model,
			"--max-session-turns", fmt.Sprint(cfg.MaxTurns), "--max-wall-time", fmt.Sprintf("%ds", int(cfg.Timeout.Seconds())), t.Prompt}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		extraEnv["QWEN_CODE_SUPPRESS_YOLO_WARNING"] = "1"
	case "hermes-agent":
		// api_key must be explicit: hermes only uses a custom base URL when
		// both base_url and api_key are set, otherwise it falls back to OpenRouter.
		argv = []string{"hermes-agent", "--query", t.Prompt, "--model", cfg.Model, "--base_url", cfg.BaseURL,
			"--api_key", cfg.Key, "--max_turns", fmt.Sprint(cfg.MaxTurns), "--enabled_toolsets", "terminal", "--verbose"}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		extraEnv["OPENROUTER_API_KEY"] = cfg.Key
	case "grok":
		argv = []string{"grok", "-p", t.Prompt, "--cwd", dir, "--always-approve", "--output-format", "json",
			"--model", cfg.Model, "--max-turns", fmt.Sprint(cfg.MaxTurns), "--disable-web-search"}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		grokHomeMu.Lock()
		home := grokHomePath
		grokHomeMu.Unlock()
		if home != "" {
			extraEnv["GROK_HOME"] = home
		}
		extraEnv["XAI_API_KEY"] = ""
	default:
		return "", "", nil, 1, fmt.Errorf("unknown harness %s", harness)
	}
	tool.Supervise(cmd) // kill the harness's whole tree on timeout; never block on an orphan
	cmd.Dir = dir
	cmd.Env = childEnv(cfg, extraEnv)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()
	return outb.String(), errb.String(), argv, exitCode(err), err
}

func probeGrok(ctx context.Context, cfg runConfig, tasks []eval.Task) ([]eval.BenchTaskResult, string, bool) {
	home, err := os.MkdirTemp("", "grok-home-dashscope-")
	if err != nil {
		rs := eval.UnavailableResults("grok", "mkdir GROK_HOME: "+err.Error(), "grok", err.Error(), tasks)
		return rs, err.Error(), false
	}
	toml := fmt.Sprintf(`[model."%s"]
model = %q
base_url = %q
name = "Qwen Dashscope"
env_key = "OPENAI_API_KEY"
api_backend = "chat_completions"
context_window = 128000
`, cfg.Model, cfg.Model, cfg.BaseURL)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o644); err != nil {
		rs := eval.UnavailableResults("grok", err.Error(), "grok", err.Error(), tasks)
		return rs, err.Error(), false
	}
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, "grok", "-p", "Reply with the single word pong and nothing else.",
		"--always-approve", "--output-format", "json", "--model", cfg.Model, "--max-turns", "2")
	tool.Supervise(cmd)
	cmd.Env = childEnv(cfg, map[string]string{
		"GROK_HOME":         home,
		"OPENAI_API_KEY":    cfg.Key,
		"OPENAI_BASE_URL":   cfg.BaseURL,
		"DASHSCOPE_API_KEY": cfg.Key,
		"XAI_API_KEY":       "",
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	out := stdout.String() + "\n" + stderr.String()
	low := strings.ToLower(out)
	aimed := strings.Contains(low, `"qwen3.7-plus"`) || strings.Contains(low, "qwen3.7-plus")
	if strings.Contains(low, `"model": "grok-4`) && !strings.Contains(low, "qwen3.7-plus") {
		aimed = false
	}
	if strings.Contains(low, cfg.BaseURL) || strings.Contains(low, "dashscope") {
		aimed = true
	}
	if err != nil && (strings.Contains(low, "unknown model") || strings.Contains(low, "unexpected argument")) {
		aimed = false
	}
	if !aimed {
		reason := "grok cannot be aimed at Dashscope qwen3.7-plus without using xAI; probe output did not confirm the custom model"
		if err != nil {
			reason += ": " + err.Error()
		}
		excerpt := trimCap(out, 2000)
		note := fmt.Sprintf("Probe command `grok -p ... --model %s` with GROK_HOME custom `[model.%s]` (env_key only, no key on disk).\n\n```\n%s\n```\n", cfg.Model, cfg.Model, excerpt)
		os.RemoveAll(home)
		return eval.UnavailableResults("grok", reason, "grok -p --model "+cfg.Model+" --output-format json", excerpt, tasks), note, false
	}
	// keep GROK_HOME for the real run; stash on cfg via pointer... cfg is a copy.
	// Re-create path is stored in a package-level so invoke can see it.
	grokHomeMu.Lock()
	grokHomePath = home
	grokHomeMu.Unlock()
	return nil, fmt.Sprintf("GROK_HOME custom model %s -> %s (env_key=OPENAI_API_KEY, key not written).\n", cfg.Model, cfg.BaseURL), true
}

var (
	grokHomeMu   sync.Mutex
	grokHomePath string
)

func probeCmd(ctx context.Context, cfg runConfig, name string, args ...string) (command, stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	tool.Supervise(cmd)
	cmd.Env = childEnv(cfg, nil)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()
	return strings.Join(append([]string{name}, args...), " "), outb.String(), errb.String(), err
}

func childEnv(cfg runConfig, extra map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+8)
	skip := map[string]bool{}
	for k := range extra {
		skip[k] = true
	}
	skip["XAI_API_KEY"] = extra != nil && extra["XAI_API_KEY"] == ""
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if skip[k] {
			continue
		}
		out = append(out, e)
	}
	for k, v := range extra {
		if v == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	_ = cfg
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func evalSanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func trimCap(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
