package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	// The results file goes in a directory this process owns and removes,
	// not loose in the temp root. A bare temp file there can be swept up
	// by whatever else cleans /tmp while a long run is still going, and
	// the run then fails with "did not write eval JSON" — which reads as
	// the harness under test misbehaving rather than as housekeeping.
	batchDir, err := os.MkdirTemp("", "janetcode-batch-")
	if err != nil {
		return eval.UnavailableResults("janetcode", "scratch batch dir: "+err.Error(), bin+" --eval", err.Error(), tasks)
	}
	defer os.RemoveAll(batchDir)
	jsonOut := filepath.Join(batchDir, fmt.Sprintf("batch%d.json", batchN))
	xdg, err := os.MkdirTemp("", "janetcode-xdg-")
	if err != nil {
		// Without a private config dir janetcode would read the operator's
		// real settings; that is not a benchmark run.
		return eval.UnavailableResults("janetcode", "scratch config dir: "+err.Error(), bin+" --eval", err.Error(), tasks)
	}
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
	err = cmd.Run()
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
		reason += fmt.Sprintf(" (batch of %d took %s; per-task wall unknown)", len(tasks), wall.Round(time.Millisecond))
		rs := eval.UnavailableResults("janetcode", reason, strings.Join(append([]string{bin}, args...), " "), excerpt, tasks)
		for i := range rs {
			rs[i].Unavailable = false // a failed run, not a missing harness
			rs[i].SetWall(0)          // never invent a per-task duration
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
	var tctx context.Context = ctx // the context of the last invocation, for the timeout check
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
	// A session task sends its prompts one by one, each as its own
	// invocation continuing the same harness session in the same tree;
	// every invocation gets the full wall budget, and the usage of each
	// is kept as a step so the report can show how the context grew.
	sess := newSession(harness, t)
	if sess != nil && sess.dir != "" {
		defer os.RemoveAll(sess.dir)
	}
	var stdout, stderr string
	var argv []string
	var code int
	var runErr error
	var total eval.UsageMetrics
	var wall time.Duration
	prompts := t.Steps()
	for i, prompt := range prompts {
		sctx, scancel := context.WithTimeout(ctx, cfg.Timeout)
		start := time.Now()
		stdout, stderr, argv, code, runErr = invoke(sctx, cfg, harness, prompt, dir, sess, i)
		stepWall := time.Since(start)
		timedOut := sctx.Err() != nil
		scancel()
		wall += stepWall
		if cfg.Scratch != "" {
			logp := filepath.Join(cfg.Scratch, fmt.Sprintf("%s-%s-p%d.log", harness, evalSanitize(t.Name), i+1))
			_ = os.WriteFile(logp, []byte(redact(stdout+"\n--- stderr ---\n"+stderr, cfg.Key)), 0o644)
		}
		u := fillUsage(eval.ParseUsageJSON([]byte(stdout)), eval.ParseUsageText(stdout+"\n"+stderr))
		total = total.Add(u)
		if len(prompts) > 1 {
			res.Steps = append(res.Steps, eval.BenchStep{Input: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Output: u.Output, Turns: u.Turns, WallTimeMS: stepWall.Milliseconds()})
		}
		if runErr != nil && timedOut {
			runErr = fmt.Errorf("prompt %d of %d: %w", i+1, len(prompts), runErr)
			tctx = sctx
			break
		}
	}
	if harness == "openclaw" && sess != nil {
		// A session keeps its profile and workspace between prompts;
		// the last prompt has run, so remove them here.
		key := filepath.Base(dir)
		openclawCleanup(openclawTaskProfile(key), filepath.Join(os.TempDir(), "openclaw-bench", evalSanitize(key)))
	}
	res.SetWall(wall)
	res.Command = redact(strings.Join(argv, " "), cfg.Key)
	res.ExitCode = code
	res.StderrExcerpt = trimCap(stderr, 1500)

	text := eval.ExtractAssistantText([]byte(stdout))
	if text == "" {
		text = strings.TrimSpace(stdout)
	}
	outPath := filepath.Join(dir, ".eval-output.txt")
	_ = os.WriteFile(outPath, []byte(text+"\n"), 0o644)
	res.ApplyUsage(total)

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

// session identifies one harness conversation across the prompts of a
// session task. Single-prompt tasks run ephemeral, as before.
type session struct {
	id  string
	dir string // pi keeps session files here, never in the operator's store
}

func newSession(harness string, t eval.Task) *session {
	if !t.Session() {
		return nil
	}
	s := &session{id: uuid4()}
	if harness == "pi" {
		if d, err := os.MkdirTemp("", "pi-sessions-"); err == nil {
			s.dir = d
		}
	}
	return s
}

// uuid4 is a random session id in the shape every harness accepts.
func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(n >> (8 * (i % 8)))
		}
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// invoke runs one prompt on a harness in dir. With a session, the first
// prompt creates the harness's session under the given id and later ones
// resume it, so the conversation carries over the way it does for a user.
// openclaw scaffolds these into any workspace it uses; they are its own
// files, not the task's, and must not be copied back as results.
var openclawScaffold = map[string]bool{
	"AGENTS.md": true, "BOOTSTRAP.md": true, "IDENTITY.md": true,
	"SOUL.md": true, "USER.md": true,
}

// openclawWorkspaceDir is one directory reused for every task in a run.
//
// Pointing openclaw at each task directory in turn does not work: it
// attests a workspace on first use and refuses to start when that
// directory later disappears — which is exactly what happens when the
// previous task's directory is cleaned up. So the workspace is stable and
// the task moves through it: seeded in before the run, results copied back
// out after, openclaw's own scaffolding left behind.
func openclawWorkspaceDir(task string) (string, error) {
	dir := filepath.Join(os.TempDir(), "openclaw-bench", evalSanitize(task))
	return dir, os.MkdirAll(dir, 0o755)
}

// openclawTaskProfile is a config namespace of its own for each task.
// openclaw keeps exactly one workspace setting per profile, and this
// benchmark runs tasks concurrently: with a shared profile the tasks
// overwrite each other's workspace between the patch and the run, and each
// one then works in some other task's directory. That is what made every
// task fail while the same command by hand succeeded.
func openclawTaskProfile(task string) string {
	return openclawProfile + "-" + evalSanitize(task)
}

// openclawSeed empties the shared workspace of the last task's files and
// copies this one's in.
func openclawSeed(ws, taskDir string) error {
	entries, err := os.ReadDir(ws)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if openclawScaffold[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(ws, e.Name())); err != nil {
			return err
		}
	}
	return copyTree(taskDir, ws, nil)
}

// openclawHarvest copies what the run produced back to the task directory,
// where the check will look for it.
func openclawHarvest(ws, taskDir string) error {
	return copyTree(ws, taskDir, openclawScaffold)
}

// copyTree copies a directory's contents, skipping top-level names in skip.
func copyTree(from, to string, skip map[string]bool) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(from, path)
		if rerr != nil || rel == "." {
			return rerr
		}
		if skip != nil {
			if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; skip[top] {
				return nil
			}
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		info, _ := d.Info()
		mode := fs.FileMode(0o644)
		if info != nil {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(dest, data, mode)
	})
}

// openclawCleanup removes what one task's run left behind: its workspace
// and its profile under ~/.openclaw-<profile>. Every task gets a profile of
// its own with a fresh suffix per run, so nothing reclaims them otherwise;
// three runs of twenty tasks had left 116 of them in the home directory.
// Only profiles this benchmark named are touched.
func openclawCleanup(profile, ws string) {
	if os.Getenv("HARNESSBENCH_KEEP") != "" {
		return
	}
	if ws != "" {
		_ = os.RemoveAll(ws)
	}
	if !strings.HasPrefix(profile, openclawProfile+"-") {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(home, ".openclaw-"+profile))
}

// openclawConfigure writes everything one task's profile needs: the
// endpoint under test, and the workspace it must work in.
func openclawConfigure(ctx context.Context, profile string, cfg runConfig, ws string) error {
	patch := map[string]any{
		"agents": map[string]any{"defaults": map[string]any{"workspace": ws}},
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
		return err
	}
	f, err := os.CreateTemp("", "openclaw-ws-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cmd := exec.CommandContext(ctx, "openclaw", "--profile", profile, "config", "patch", "--replace-path", "models.providers.bench.models", "--file", f.Name())
	cmd.Env = openclawEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// piProvider names the pi provider that serves the endpoint under test.
// pi reaches an endpoint through a provider entry, not a base URL: its
// built-in "openrouter" for OpenRouter, and a "dashscope" entry in the
// user's models.json for DashScope, which must list the model by id.
func piProvider(baseURL string) string {
	host := strings.ToLower(baseURL)
	switch {
	case strings.Contains(host, "openrouter"):
		return "openrouter"
	case strings.Contains(host, "deepseek.com"):
		return "deepseek"
	}
	return "dashscope"
}

// hermes-agent has no non-interactive continuation; it runs each prompt
// fresh and the report says so.
func invoke(ctx context.Context, cfg runConfig, harness string, prompt, dir string, sess *session, step int) (stdout, stderr string, argv []string, code int, err error) {
	var cmd *exec.Cmd
	var openclawWS string
	extraEnv := map[string]string{
		"OPENAI_API_KEY":    cfg.Key,
		"OPENAI_BASE_URL":   cfg.BaseURL,
		"DASHSCOPE_API_KEY": cfg.Key,
	}
	switch harness {
	case "pi":
		argv = []string{"pi", "-p", "--mode", "json", "--provider", piProvider(cfg.BaseURL), "--model", cfg.Model}
		if sess == nil {
			argv = append(argv, "--no-session")
		} else {
			argv = append(argv, "--session-id", sess.id)
			if sess.dir != "" {
				argv = append(argv, "--session-dir", sess.dir)
			}
		}
		argv = append(argv, prompt)
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	case "qwencode":
		argv = []string{"qwen", "--yolo", "--output-format", "json", "--auth-type", "openai",
			"--openai-base-url", cfg.BaseURL, "--model", cfg.Model,
			"--max-session-turns", fmt.Sprint(cfg.MaxTurns), "--max-wall-time", fmt.Sprintf("%ds", int(cfg.Timeout.Seconds()))}
		if sess != nil {
			argv = append(argv, "--chat-recording")
			if step == 0 {
				argv = append(argv, "--session-id", sess.id)
			} else {
				argv = append(argv, "--resume", sess.id)
			}
		}
		argv = append(argv, prompt)
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		extraEnv["QWEN_CODE_SUPPRESS_YOLO_WARNING"] = "1"
	case "hermes-agent":
		// api_key must be explicit: hermes only uses a custom base URL when
		// both base_url and api_key are set, otherwise it falls back to OpenRouter.
		argv = []string{"hermes-agent", "--query", prompt, "--model", cfg.Model, "--base_url", cfg.BaseURL,
			"--api_key", cfg.Key, "--max_turns", fmt.Sprint(cfg.MaxTurns), "--enabled_toolsets", "terminal", "--verbose"}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		extraEnv["OPENROUTER_API_KEY"] = cfg.Key
		// hermes-agent's terminal tool takes its directory from
		// TERMINAL_CWD, not from the process working directory. Without
		// it every task runs in the user's home: the agent does the work
		// correctly and the check then looks for the files in the task
		// directory and finds nothing. It scored 0/20 here for that
		// reason alone, which is a bug in this launcher and not in it.
		extraEnv["TERMINAL_CWD"] = dir
		extraEnv["TERMINAL_ENV"] = "local"
	case "openclaw":
		// openclaw works in its own workspace, not the process's working
		// directory, so the workspace is repointed at this task before
		// every run. The session id is fresh each time too: openclaw
		// otherwise remembers the previous task and answers "already
		// done" without looking at the directory it is now in.
		// The task directory's name is unique per task and stable for the
		// life of the run, which is exactly the key both need.
		key := filepath.Base(dir)
		profile := openclawTaskProfile(key)
		ws, werr := openclawWorkspaceDir(key)
		if werr == nil {
			werr = openclawConfigure(ctx, profile, cfg, ws)
		}
		if werr == nil {
			werr = openclawSeed(ws, dir)
		}
		if werr != nil {
			return "", "", nil, 0, fmt.Errorf("openclaw workspace: %w", werr)
		}
		openclawWS = ws
		defer func() {
			// Whatever the run produced lives in the shared workspace;
			// the check looks in the task directory.
			_ = openclawHarvest(openclawWS, dir)
			if sess == nil {
				openclawCleanup(profile, openclawWS)
			}
		}()
		extraEnv["PATH"] = "/usr/bin:/usr/local/bin:" + os.Getenv("PATH")
		// A session's prompts share one id so openclaw continues the
		// conversation the way pi and qwencode do; a single-prompt task
		// gets a fresh one so nothing from the last task carries over.
		sessionID := fmt.Sprintf("bench-%d-%d", step, time.Now().UnixNano())
		if sess != nil {
			sessionID = "bench-" + sess.id
		}
		argv = []string{"openclaw", "--profile", profile, "agent", "--local", "--json",
			"--model", "bench/" + cfg.Model,
			"--session-id", sessionID,
			"--message", prompt}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	case "claudecode":
		// Claude Code speaks to Anthropic on its own credentials; the
		// endpoint flags do not apply to it, and the model is named the
		// way it names them.
		argv = []string{"claude", "-p", prompt, "--output-format", "json",
			"--dangerously-skip-permissions", "--max-turns", fmt.Sprint(cfg.MaxTurns)}
		if cfg.ClaudeModel != "" {
			argv = append(argv, "--model", cfg.ClaudeModel)
		}
		if sess != nil && step > 0 {
			argv = append(argv, "--resume", sess.id)
		} else if sess != nil {
			argv = append(argv, "--session-id", sess.id)
		}
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		// Its own key, not the benchmark's endpoint.
		delete(extraEnv, "OPENAI_API_KEY")
		delete(extraEnv, "OPENAI_BASE_URL")
		delete(extraEnv, "DASHSCOPE_API_KEY")
	case "grok":
		argv = []string{"grok", "-p", prompt, "--cwd", dir, "--always-approve", "--output-format", "json",
			"--model", cfg.Model, "--max-turns", fmt.Sprint(cfg.MaxTurns), "--disable-web-search"}
		if sess != nil {
			if step == 0 {
				argv = append(argv, "--session-id", sess.id)
			} else {
				argv = append(argv, "--resume", sess.id)
			}
		}
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
	if openclawWS != "" {
		// openclaw is run from inside its own workspace: configuring the
		// workspace is not enough on its own.
		cmd.Dir = openclawWS
	}
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
	cmd.Dir = home // auto-approve must never run in the real repository
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
	model := strings.ToLower(cfg.Model)
	aimed := strings.Contains(low, model)
	if strings.Contains(low, `"model": "grok-4`) && !strings.Contains(low, model) {
		aimed = false
	}
	if strings.Contains(low, cfg.BaseURL) || strings.Contains(low, "dashscope") {
		aimed = true
	}
	if err != nil && (strings.Contains(low, "unknown model") || strings.Contains(low, "unexpected argument")) {
		aimed = false
	}
	if !aimed {
		reason := "grok cannot be aimed at " + cfg.Model + " on " + cfg.BaseURL + " without using xAI; probe output did not confirm the custom model"
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

// cleanupGrokHome removes the temporary GROK_HOME after the run.
func cleanupGrokHome() {
	grokHomeMu.Lock()
	defer grokHomeMu.Unlock()
	if grokHomePath != "" {
		os.RemoveAll(grokHomePath)
		grokHomePath = ""
	}
}

func probeCmd(ctx context.Context, cfg runConfig, name string, args ...string) (command, stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	tool.Supervise(cmd)
	cmd.Dir = os.TempDir() // never the repository
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
	// Every key in extra replaces the inherited value; an empty value
	// unsets it (XAI_API_KEY for grok, so it cannot fall back to xAI).
	skip := map[string]bool{}
	for k := range extra {
		skip[k] = true
	}
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
