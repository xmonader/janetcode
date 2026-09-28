package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
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
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

var (
	skSp    = regexp.MustCompile(`sk-sp-\S+`)
	skOther = regexp.MustCompile(`sk-[A-Za-z0-9.*]{8,}`)
)

func runJanetBatch(ctx context.Context, cfg runConfig, batchN int, tasks []eval.Task, logW io.Writer) []eval.BenchTaskResult {
	bin := janetBin(cfg)
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
	home, cleanup, err := janetHome()
	if err != nil {
		// Without a private config dir janetcode would read the operator's
		// real settings; that is not a benchmark run.
		return eval.UnavailableResults("janetcode", "scratch config dir: "+err.Error(), bin+" --eval", err.Error(), tasks)
	}
	defer cleanup()

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
	shell.Supervise(cmd)
	cmd.Dir = cfg.RepoRoot
	cmd.Env = childEnv(janetEnv(cfg, home))
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
	if err := seedWorkdir(ctx, t, dir); err != nil {
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
	env := &taskEnv{}
	defer env.close()
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
		stdout, stderr, argv, code, runErr = invoke(sctx, cfg, harness, prompt, dir, sess, env, i)
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
		t.Name, res.Passed, res.WallTime, res.InputTokens, res.CacheReadTokens, res.OutputTokens, textfmt.FirstLine(strings.TrimSpace(res.Reason), 0))
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

func seedWorkdir(ctx context.Context, t eval.Task, dir string) error {
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
		// Under ctx like every other subprocess here: a ctrl-c during
		// setup must not leave a git waiting on a lock nobody is
		// watching for.
		_ = exec.CommandContext(ctx, "git", "-C", dir, "init", "-q").Run()
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
	shell.Supervise(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EVAL_OUTPUT="+outPath, "EVAL_DIR="+dir)
	out, err := cmd.CombinedOutput()
	// Supervise cut the pipe wait short on a child the check left running;
	// the check itself exited 0, and the native runner scores that a pass.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
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

// taskEnv is the state one task's invocations share. Today that is
// janetcode's private home: the sessions, memory and config a headless
// run reads live under it, so a fresh one per prompt means the second
// prompt of a session task cannot find the first prompt's session at all
// — the whole point of the session set. It is created lazily, since only
// the janetcode driver needs one.
type taskEnv struct {
	janetHomeDir string
	cleanups     []func()
}

func (e *taskEnv) janetHome() (string, error) {
	if e.janetHomeDir != "" {
		return e.janetHomeDir, nil
	}
	dir, cleanup, err := janetHome()
	if err != nil {
		return "", err
	}
	e.janetHomeDir = dir
	e.cleanups = append(e.cleanups, cleanup)
	return dir, nil
}

func (e *taskEnv) close() {
	for _, c := range e.cleanups {
		c()
	}
	e.cleanups = nil
	e.janetHomeDir = ""
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
func invoke(ctx context.Context, cfg runConfig, harness string, prompt, dir string, sess *session, env *taskEnv, step int) (stdout, stderr string, argv []string, code int, err error) {
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
	case "janetcode":
		// Driven exactly as the other CLIs are, reading the result object
		// janetcode prints for itself rather than its eval results file.
		//
		// The limits are the ones the eval driver passes, spelled with the
		// everyday flags: the same model-call cap, and the tasks'
		// max_tool_calls left unenforced (a headless run never enforces
		// them, which is what --eval-no-tool-limit buys on the other side).
		// Without the cap the two drivers ran the same tasks on different
		// budgets -- 40 model calls against the configured 50 -- and a task
		// that failed on only one of them said nothing about either.
		argv = janetArgv(cfg, prompt, sess != nil, step)
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		// A headless run is the whole product: it reads the operator's
		// config, sessions, memory, skills, MCP servers and permission
		// rules from the real home directory. The eval driver has run in a
		// private one since it was written, and a comparison between the
		// two only says something about janetcode if both start from
		// nothing. The home is per task, not per prompt — a fresh one
		// between the prompts of a session would take the session with it.
		jhome, herr := env.janetHome()
		if herr != nil {
			return "", "", nil, 0, fmt.Errorf("janetcode private home: %w", herr)
		}
		for k, v := range janetEnv(cfg, jhome) {
			extraEnv[k] = v
		}
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
	shell.Supervise(cmd) // kill the harness's whole tree on timeout; never block on an orphan
	cmd.Dir = dir
	if openclawWS != "" {
		// openclaw is run from inside its own workspace: configuring the
		// workspace is not enough on its own.
		cmd.Dir = openclawWS
	}
	cmd.Env = childEnv(extraEnv)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()
	return outb.String(), errb.String(), argv, exitCode(err), err
}

// janetArgv is the headless janetcode command line for one prompt of a
// task: the everyday flags spelled to match the limits the eval driver
// passes, plus the continuation flag when this is not the task's first
// prompt.
//
// The continuation is --continue, which reopens the latest session for
// the working directory — exactly what the previous prompt left behind.
// It used to be `--resume <sess.id>`, and sess.id is a uuid this
// launcher invents for the harnesses that take one: it named a janetcode
// session that had never existed, so every prompt after the first began
// from nothing and the session set, whose entire subject is what a
// harness carries between prompts, measured janetcode as if it carried
// nothing.
func janetArgv(cfg runConfig, prompt string, session bool, step int) []string {
	argv := []string{janetBin(cfg), "-p", prompt, "--yolo", "--output-format", "json",
		"--provider", "openai", "--model", cfg.Model,
		"--max-turns", fmt.Sprint(cfg.MaxTurns)}
	if session && step > 0 {
		argv = append(argv, "--continue")
	}
	return argv
}

// janetBin is the janetcode binary as an absolute path. Every harness
// runs with its working directory set to the task's, so the default
// "bin/janetcode" resolves against that directory and not against the
// repository: the eval driver had always made it absolute, the headless
// one had not, and every headless task died instantly with an empty log
// and exit 1 -- which reads as janetcode producing nothing rather than as
// janetcode never having been started.
func janetBin(cfg runConfig) string {
	bin := cfg.JanetBin
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(cfg.RepoRoot, bin)
	}
	return bin
}

// janetHome is a private home for one janetcode invocation: its config,
// its sessions, its memory and its caches, none of which are the
// operator's. Both janetcode drivers call it, so neither can drift into
// reading the real one.
func janetHome() (string, func(), error) {
	dir, err := os.MkdirTemp("", "janetcode-home-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// janetEnv is the environment both janetcode drivers run with: the
// endpoint under test, and the private home janetHome made. It is one
// function so that a variable added for one driver reaches the other.
func janetEnv(cfg runConfig, home string) map[string]string {
	return map[string]string{
		"HOME":               home,
		"XDG_CONFIG_HOME":    filepath.Join(home, "config"),
		"XDG_DATA_HOME":      filepath.Join(home, "data"),
		"XDG_CACHE_HOME":     filepath.Join(home, "cache"),
		"OPENAI_API_KEY":     cfg.Key,
		"OPENAI_BASE_URL":    cfg.BaseURL,
		"DASHSCOPE_API_KEY":  cfg.Key,
		"JANETCODE_PROVIDER": "openai",
		"JANETCODE_MODEL":    cfg.Model,
	}
}

func childEnv(extra map[string]string) []string {
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

// trimCap is the tail of an excerpt: what a failing harness printed last
// is what says why it failed. textfmt.TailBytes keeps it valid UTF-8 —
// slicing the bytes by hand put a replacement character at the start of
// every clipped excerpt that happened to cut a rune.
func trimCap(s string, n int) string {
	return textfmt.TailBytes(strings.TrimSpace(s), n)
}
