// Package eval runs small end-to-end tasks against a real provider and
// reports pass/fail, cost and tool-call counts. It is the regression test
// for the agent's behaviour, as opposed to its code: a prompt change that
// makes the model stop reading files before editing shows up here.
package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/worktree"
)

// Task is one eval case, stored as JSON.
type Task struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	// Prompts makes the task a session: each prompt is sent in turn to the
	// same conversation in the same work directory, and Check runs after
	// the last. Prompt may then be empty. Sessions are where context
	// management shows: the history grows across prompts, so what a
	// harness keeps, drops and summarises decides both the token bill and
	// whether the later prompts still know what the earlier ones did.
	Prompts []string `json:"prompts,omitempty"`
	// Files are written into the work directory before the run.
	Files map[string]string `json:"files,omitempty"`
	// Locked names Files entries that are written again, from Files, just
	// before Check runs, so a run cannot pass by editing them: /eval keep
	// stores a session's locked /accept acceptance tests this way.
	Locked []string `json:"locked,omitempty"`
	// HiddenFiles are held-out acceptance tests: files the task ships and
	// the runner never writes into the work directory. They are laid over
	// a scratch copy of the workspace by the held-out gate alone (see
	// hidden.go), so the model can neither read them nor run them, and a
	// gate failure is the first thing in this repository's evals that the
	// model cannot pre-empt by running the grading tests itself.
	HiddenFiles map[string]string `json:"hidden,omitempty"`
	// HiddenCheck is the command the held-out gate runs in the scratch
	// copy; empty means `go test ./acceptance/`.
	HiddenCheck string `json:"hidden_check,omitempty"`
	// Reference is a solution kept beside the task and never written into
	// a work directory: laid over Files it must make the held-out tests
	// pass, exactly as Files left untouched must make them fail. Both
	// halves are checked offline (see TestHiddenSetIsDiscriminating). A
	// held-out set nobody has proved satisfiable is not an instrument.
	Reference map[string]string `json:"reference,omitempty"`
	// Check is a shell command run in the work directory afterwards; exit
	// 0 passes. EVAL_OUTPUT names a file holding the model's final text,
	// and EVAL_HIDDEN_GATE the held-out gate script when the task has one.
	Check string `json:"check"`
	// Timeout in seconds for the model run; zero means 180.
	Timeout int `json:"timeout,omitempty"`
	// MaxToolCalls fails the task when exceeded; zero means no limit.
	MaxToolCalls int `json:"max_tool_calls,omitempty"`
	// Tags let a run select a subset.
	Tags []string `json:"tags,omitempty"`
	// Repo, Base and Patch make the task's work dir a real repository
	// state instead of an empty temp dir: a detached git worktree of Repo
	// at commit Base, with Patch applied on top. /eval keep writes these
	// to turn a session into a task. Repo and Patch are relative to the
	// task file's directory (see dir) unless absolute.
	Repo  string `json:"repo,omitempty"`
	Base  string `json:"base,omitempty"`
	Patch string `json:"patch,omitempty"`
	// AllowRefs lets the run create git refs outside janetcode's own
	// namespaces — a task that asks the model to commit on an unborn
	// branch makes a branch. Without it a new branch, tag or stash
	// marks the run POLLUTED (see pollution.go).
	AllowRefs bool `json:"allow_refs,omitempty"`
	// dir is the directory the task was loaded from, set by Load, so Repo
	// and Patch resolve relative to the task rather than the process's
	// cwd. Zero value for a task built in memory (e.g. by a test).
	dir string `json:"-"`
}

// RepoPath is Repo resolved against the task's source directory, always
// as an absolute path (see resolvePath).
func (t Task) RepoPath() string { return t.resolvePath(t.Repo) }

// PatchPath is Patch resolved against the task's source directory, always
// as an absolute path (see resolvePath).
func (t Task) PatchPath() string { return t.resolvePath(t.Patch) }

// resolvePath joins p onto the task's source directory when p is
// relative, then makes the result absolute. Absolute is not optional
// here: callers hand this straight to `git -C <workdir> apply <path>`,
// and -C changes git's working directory before it opens path, so a
// path left relative to the process's cwd (or to a relative t.dir) is
// silently wrong the moment the caller's cwd differs from where the task
// was loaded from. filepath.Abs resolves against the process cwd, which
// is fine: by the time a task is loaded, Load has already made t.dir
// absolute, so a relative Repo/Patch only reaches Abs already joined
// onto that absolute dir.
func (t Task) resolvePath(p string) string {
	if p == "" {
		return p
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.dir, p)
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// Load reads one JSON file or every *.json in a directory, sorted by name.
func Load(path string) ([]Task, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var files []string
	if st.IsDir() {
		if files, err = filepath.Glob(filepath.Join(path, "*.json")); err != nil {
			return nil, err
		}
		sort.Strings(files)
	} else {
		files = []string{path}
	}
	var tasks []Task
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if t.Name == "" {
			t.Name = strings.TrimSuffix(filepath.Base(f), ".json")
		}
		if len(t.Steps()) == 0 || strings.TrimSpace(t.Check) == "" {
			return nil, fmt.Errorf("%s: prompt and check are required (prompts may stand in for prompt)", f)
		}
		for _, name := range t.Locked {
			if _, ok := t.Files[name]; !ok {
				return nil, fmt.Errorf("%s: locked file %q is not in files", f, name)
			}
		}
		if err := checkHiddenTask(t); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("%s: duplicate task name %q", f, t.Name)
		}
		seen[t.Name] = true
		t.dir = filepath.Dir(f)
		// f, and so t.dir, is relative when path was (e.g. the exact
		// ".janetcode/evals" /eval keep prints); make it absolute now so
		// RepoPath/PatchPath stay correct even if the caller's cwd
		// changes before it uses them (see resolvePath).
		if abs, aerr := filepath.Abs(t.dir); aerr == nil {
			t.dir = abs
		}
		expandLarge(&t)
		tasks = append(tasks, t)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks in %s", path)
	}
	return tasks, nil
}

// Steps is the sequence of prompts the task sends: Prompts for a session,
// else the single Prompt. Blank prompts are dropped.
func (t Task) Steps() []string {
	var out []string
	for _, p := range t.Prompts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 && strings.TrimSpace(t.Prompt) != "" {
		out = append(out, t.Prompt)
	}
	return out
}

// Session reports whether the task is a multi-prompt session.
func (t Task) Session() bool { return len(t.Steps()) > 1 }

// Filter keeps tasks whose name or tags match any of the words.
func Filter(tasks []Task, words []string) []Task {
	if len(words) == 0 {
		return tasks
	}
	var out []Task
	for _, t := range tasks {
		for _, w := range words {
			if t.Name == w || strings.Contains(t.Name, w) || contains(t.Tags, w) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// Result is the outcome of one task.
type Result struct {
	Name     string        `json:"name"`
	Run      int           `json:"run"` // 0-based repetition index
	Passed   bool          `json:"passed"`
	Reason   string        `json:"reason,omitempty"`
	Duration time.Duration `json:"duration"`
	// ModelTime is time spent waiting on the provider; ToolTime is time
	// spent running tools. They do not sum to Duration — checks, setup and
	// the harness itself are the rest.
	ModelTime time.Duration `json:"model_time,omitempty"`
	ToolTime  time.Duration `json:"tool_time,omitempty"`
	ToolCalls int           `json:"tool_calls"`
	Turns     int           `json:"turns"` // model calls
	// Recoveries counts tool calls parsed back out of assistant text,
	// so a model that writes its calls as prose is visible in a run
	// rather than looking like one that simply did nothing. WatchdogKills
	// and ModelFallbacks are the other two ways a run was rescued rather
	// than smooth: a stream that stopped producing and was ended, and a
	// call that only succeeded on another model. A passing run with three
	// of these is not the same run as one with none, and a comparison
	// that cannot see them credits the model with the harness's work.
	Recoveries     int       `json:"recoveries,omitempty"`
	WatchdogKills  int       `json:"watchdog_kills,omitempty"`
	ModelFallbacks int       `json:"model_fallbacks,omitempty"`
	Usage          llm.Usage `json:"usage"`
	CostUSD        float64   `json:"cost_usd"`
	CostKnown      bool      `json:"cost_known"`
	// Compaction is what the context pipeline did during the run: how often
	// the history was summarised, what the summariser calls cost (a subset
	// of Usage), and how many archived outputs the model fetched back.
	Compaction Compaction `json:"compaction"`
	// Nudges is how many bash results carried a hint toward a dedicated
	// tool (JANETCODE_OFF=toolnudge turns it off), so an A/B of tool
	// adoption can see the lever fire.
	Nudges int `json:"tool_nudges,omitempty"`
	// Steps is the per-prompt accounting of a session task, in order.
	Steps []Step `json:"steps,omitempty"`
	// Facts is the fact-survival probe of the run, when the runner asked
	// for one and the run compacted something to probe.
	Facts    *FactProbe `json:"facts,omitempty"`
	Output   string     `json:"output"`
	CheckOut string     `json:"check_output,omitempty"`
	Dir      string     `json:"dir,omitempty"`
	// Features is the agent's trajectory at the end of the run, the
	// training data for the success predictor (agent.Train).
	Features map[string]float64 `json:"features,omitempty"`
	// Decisions is what the optional decision provider did during the
	// run. A consumer that silently could not call it — a wrong model
	// slug, a missing key, an opened circuit — is otherwise invisible in
	// a result file, and an arm measured that way measures nothing.
	Decisions DecisionStats `json:"decisions,omitempty"`
	// Events is the append-only trajectory log when the agent had a Log.
	Events []session.Event `json:"events,omitempty"`
	// Infra marks a run that ended for a reason outside the model — the
	// provider refusing (429, 5xx, an exhausted balance), the network, a
	// stream that stayed dead through the retries — or that never got a
	// single answer from it. The model did not fail it, so it counts in
	// neither the numerator nor the denominator of a pass rate; Reason
	// says what happened.
	Infra bool `json:"infra,omitempty"`
	// Polluted lists what the run left in the work directory that
	// janetcode itself made — a .janetcode/ path or a git ref the task did
	// not ask for (pollution.go). A polluted run is a failure.
	Polluted []string `json:"polluted,omitempty"`
	// Mismatches counts the run's model calls whose response named another
	// vendor than the requested model's (internal/provenance), and
	// Mismatch is the first verdict. A run with any measured another
	// model, and is left out of the scores unless the runner was asked to
	// fail the whole eval instead.
	Mismatches int    `json:"mismatches,omitempty"`
	Mismatch   string `json:"mismatch,omitempty"`
	// Served is the provenance of the run's last model call: who the
	// responses said served it.
	Served *llm.Provenance `json:"served,omitempty"`
}

// Scored reports whether a run counts toward pass rates and per-task
// comparisons: it reached the model, and the model that answered was the
// one asked for.
func (r Result) Scored() bool { return !r.Infra && r.Mismatches == 0 }

// Outcome is the run's outcome in one word: PASS, FAIL, INFRA (never
// reached the model or ended outside it) or MISMATCH (another vendor's
// model answered).
func (r Result) Outcome() string {
	switch {
	case r.Mismatches > 0:
		return "MISMATCH"
	case r.Infra:
		return "INFRA"
	case len(r.Polluted) > 0:
		return "POLLUTED"
	case r.Passed:
		return "PASS"
	}
	return "FAIL"
}

// DecisionStats is one run's use of the decision provider.
type DecisionStats struct {
	Calls   int           `json:"calls,omitempty"`
	Errors  int           `json:"errors,omitempty"`
	Acted   int           `json:"acted,omitempty"`
	Time    time.Duration `json:"time,omitempty"`
	CostUSD float64       `json:"cost_usd,omitempty"`
}

// Compaction summarises the context pipeline's work in one run.
type Compaction struct {
	Count   int       `json:"count"`
	Summary llm.Usage `json:"summary_usage"`
	Expands int       `json:"expands"`
}

// Step is one prompt of a session: what it cost and how big the context
// was when it ended. Context is the prompt size of the step's last model
// call (input plus cache tokens), the number that shows whether history is
// growing or being held down.
type Step struct {
	Usage       llm.Usage `json:"usage"`
	Turns       int       `json:"turns"`
	ToolCalls   int       `json:"tool_calls"`
	Compactions int       `json:"compactions"`
	Context     int       `json:"context"`
	Peak        int       `json:"peak"` // largest single model call
}

// FoldSteps reads per-turn accounting from a run's event log. Turn n of
// the log is prompt n of the session.
func FoldSteps(events []session.Event) []Step {
	var steps []Step
	at := func(turn int) *Step {
		for len(steps) < turn {
			steps = append(steps, Step{})
		}
		return &steps[turn-1]
	}
	for _, e := range events {
		if e.Turn <= 0 {
			continue
		}
		switch e.Type {
		case session.TypeAssistant:
			st := at(e.Turn)
			st.Turns++
			if e.Usage != nil {
				st.Usage.Add(*e.Usage)
				st.Context = contextTokens(*e.Usage)
				st.Peak = max(st.Peak, st.Context)
			}
		case session.TypeToolCall:
			at(e.Turn).ToolCalls++
		case session.TypeCompactSummary:
			st := at(e.Turn)
			st.Compactions++
			if e.Usage != nil {
				st.Usage.Add(*e.Usage)
			}
		}
	}
	return steps
}

// FoldNudges counts the tool results that carry a bash hint toward a
// dedicated tool.
func FoldNudges(events []session.Event) int {
	n := 0
	for _, e := range events {
		if e.Type == session.TypeToolResult && strings.Contains(e.Output, tool.NudgeMarker) {
			n++
		}
	}
	return n
}

// FoldCompaction reads the compaction metrics from a run's event log.
func FoldCompaction(events []session.Event) Compaction {
	var c Compaction
	for _, e := range events {
		switch e.Type {
		case session.TypeCompactSummary:
			c.Count++
			if e.Usage != nil {
				c.Summary.Add(*e.Usage)
			}
		case session.TypeToolCall:
			if e.Name == "expand_output" {
				c.Expands++
			}
		}
	}
	return c
}

// Runner executes tasks with fresh agents.
type Runner struct {
	// NewAgent builds an agent whose tools are rooted at dir. hiddenGate
	// is the held-out acceptance gate's script for this run, or "" when
	// the task ships no held-out tests; the caller appends it to the
	// verification gate (see hidden.go).
	NewAgent func(dir, hiddenGate string) (*agent.Agent, error)
	// Keep leaves work directories on disk and records them in Result.Dir.
	Keep bool
	// Log receives one line per event — a task starting, its progress
	// while it runs, its result; nil is quiet.
	Log io.Writer
	// ProgressEvery is how often a running task reports its elapsed time
	// and tool calls to Log; zero means every 30 seconds. A real model can
	// take minutes over one task, and a runner that says nothing until
	// the task ends looks hung.
	ProgressEvery time.Duration
	// Jobs is how many tasks run at once; zero means 1.
	Jobs int
	// Runs repeats every task this many times; zero means 1. Repeats
	// turn a coin flip into a rate.
	Runs int
	// Timeout, when set, replaces every task's own timeout so a benchmark
	// can give each harness the same wall budget.
	Timeout time.Duration
	// IgnoreToolLimit skips the tasks' max_tool_calls checks, which other
	// harnesses in a comparison do not enforce.
	IgnoreToolLimit bool
	// Facts, when positive, probes up to that many facts mined from what
	// compaction removed, after the run (see MineFacts, ProbeFacts).
	Facts int
	// FactsBlind withholds expand_output from the probes, so they measure
	// what the compacted context alone retains.
	FactsBlind bool
}

// Run executes every task (Runs times each) and returns results in task
// order, repetitions of a task adjacent.
func (r *Runner) Run(ctx context.Context, tasks []Task) []Result {
	jobs := r.Jobs
	if jobs <= 0 {
		jobs = 1
	}
	runs := r.Runs
	if runs <= 0 {
		runs = 1
	}
	results := make([]Result, len(tasks)*runs)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, t := range tasks {
		for run := 0; run < runs; run++ {
			wg.Add(1)
			go func(idx, run int, t Task) {
				defer wg.Done()
				// Every task-run is queued at once and waits here for a
				// slot, so a cancelled run (ctrl-c, a --max-cost stop)
				// must be caught on the way in: without this the whole
				// queue drains through r.one, each one preparing a work
				// directory and a git worktree only to fail on the dead
				// context.
				cancelled := func() bool {
					// Checked before the select as well as in it: a
					// select whose two cases are both ready picks at
					// random, so a free slot would let a task through a
					// context that is already dead.
					// A run cancelled before it started never reached the
					// model — a --max-cost stop is the usual cause — so it
					// is infrastructure, not a failure: hero_agent once
					// turned sixteen tasks into failures with a spend cap.
					if err := ctx.Err(); err != nil {
						results[idx] = Result{Name: t.Name, Run: run, Infra: true, Reason: "cancelled before it started: " + err.Error()}
						return true
					}
					select {
					case sem <- struct{}{}:
						return false
					case <-ctx.Done():
						results[idx] = Result{Name: t.Name, Run: run, Infra: true, Reason: "cancelled before it started: " + ctx.Err().Error()}
						return true
					}
				}()
				if cancelled {
					return
				}
				defer func() { <-sem }()
				label := t.Name
				if runs > 1 {
					label = fmt.Sprintf("%s #%d", t.Name, run+1)
				}
				r.logf("start %s", label)
				res := r.one(ctx, t, label)
				res.Run = run
				results[idx] = res
				r.logf("%s %s (%s, %d tool calls, %s)", res.Outcome(), label, res.Duration.Round(100*time.Millisecond), res.ToolCalls, costString(res))
				switch {
				case res.Mismatches > 0:
					r.logf("    %s", res.Mismatch)
				case !res.Passed:
					r.logf("    %s", textfmt.FirstLine(res.Reason, 0))
				}
			}(i*runs+run, run, t)
		}
	}
	wg.Wait()
	return results
}

// prepareWorkDir creates the directory a run works in. An ordinary task
// gets a plain temporary directory, git-initialised when git is
// available so tools that expect a repository still find one; a Patch
// with no Base (an unborn repository, or a self-contained kept task —
// see internal/eval/keep.go) is applied there too, since it is a diff
// against the empty tree rather than against Base. A task with Base set
// (one kept from a session by /eval keep) gets a detached git worktree of
// Repo at Base instead, with Patch applied, so the run sees the exact
// tree the session started from rather than an empty one. Either way the
// caller is responsible for calling the returned cleanup. The checkout
// itself is internal/worktree's (Scratch, Detached), shared with
// --what-if's worktree.AtStart.
func prepareWorkDir(ctx context.Context, t Task) (dir string, cleanup func(), err error) {
	if t.Base != "" {
		return prepareWorktree(ctx, t)
	}
	patch, err := readPatchFile(t.PatchPath())
	if err != nil {
		return "", nil, err
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		if _, gerr := exec.LookPath("git"); gerr != nil {
			return "", nil, fmt.Errorf("task %s has a patch but git is not available", t.Name)
		}
	}
	return worktree.Scratch(ctx, "eval-"+t.Name, patch)
}

// prepareWorktree is prepareWorkDir's path for a kept task with a Base:
// it checks out Repo at the detached commit Base into a fresh temporary
// path and applies Patch, the diff between that commit and the work tree
// snapshotted when the session began (see internal/eval/keep.go).
// Factored out from prepareWorkDir so a test can check the resulting
// tree without running a model.
func prepareWorktree(ctx context.Context, t Task) (dir string, cleanup func(), err error) {
	repo := t.RepoPath()
	if repo == "" {
		return "", nil, fmt.Errorf("task %s: base %q is set but repo is empty", t.Name, t.Base)
	}
	patch, err := readPatchFile(t.PatchPath())
	if err != nil {
		return "", nil, err
	}
	dir, cleanup, err = worktree.Detached(ctx, "eval-"+t.Name, repo, t.Base, patch)
	if err != nil {
		var missing *worktree.MissingBaseError
		if errors.As(err, &missing) {
			return "", nil, fmt.Errorf("task %s: %s: fetch it or re-keep with --self-contained (%s)", t.Name, missing.Error(), missing.Detail)
		}
		return "", nil, fmt.Errorf("task %s: %w", t.Name, err)
	}
	return dir, cleanup, nil
}

// readPatchFile is a Task's Patch, read into memory, or nil when there is
// none.
func readPatchFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read patch %s: %w", path, err)
	}
	return b, nil
}

// writeFiles writes a task's Files into dir before a run starts.
func writeFiles(dir string, files map[string]string) error {
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		// err is declared here, not in the if: an err scoped to the if
		// leaves the check below reading the outer err, which would be
		// nil, and the failure would go unreported.
		err := os.MkdirAll(filepath.Dir(p), 0o755)
		if err == nil {
			err = os.WriteFile(p, []byte(content), 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// restoreLocked writes a task's Locked files back into dir from Files,
// undoing whatever the run did to them before the check reads them.
func restoreLocked(dir string, t Task) error {
	locked := make(map[string]string, len(t.Locked))
	for _, name := range t.Locked {
		locked[name] = t.Files[name]
	}
	return writeFiles(dir, locked)
}

// runCheck runs a task's Check in dir, with EVAL_OUTPUT naming outPath
// and EVAL_HIDDEN_GATE the held-out gate script, so a task graded on
// held-out tests runs exactly the gate the turn ran.
func runCheck(ctx context.Context, t Task, dir, outPath, hiddenGate string) (string, error) {
	cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
	defer ccancel()
	cmd := shell.Command(cctx, t.Check)
	// Supervised, as verify's commands are: without a process group the
	// timeout kills the shell alone, and without a bounded pipe wait a
	// child the check leaves running holds CombinedOutput open for as long
	// as it lives — past the timeout and past a cancelled run.
	shell.Supervise(cmd)
	cmd.Dir = dir
	// Scrubbed of the agent's credentials: a task's check is project
	// content, run and read back with its output.
	cmd.Env = append(shell.ScrubEnv(os.Environ()), "EVAL_OUTPUT="+outPath, "EVAL_DIR="+dir, "EVAL_HIDDEN_GATE="+hiddenGate)
	out, err := cmd.CombinedOutput()
	// The check exited 0 and only a leaked child's hold on the pipe was
	// cut: the check passed.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return strings.TrimSpace(string(out)), err
}

// progress logs label's elapsed time and tool calls every ProgressEvery
// until the returned stop is called.
func (r *Runner) progress(label string, start time.Time, calls *atomic.Int64) (stop func()) {
	if r.Log == nil {
		return func() {}
	}
	every := r.ProgressEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				r.logf("  … %s: %s elapsed, %s", label, textfmt.Duration(time.Since(start)), textfmt.Plural(int(calls.Load()), "tool call"))
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

// one runs a task once; label names it in progress lines.
func (r *Runner) one(ctx context.Context, t Task, label string) (res Result) {
	res = Result{Name: t.Name}
	start := time.Now()
	// Registered first, so it unwinds last: every other defer below —
	// the work directory's cleanup above all — has already run by the
	// time this recovers. One task that panics (a malformed tool schema,
	// a nil provider, a bug in the agent) used to take the whole run with
	// it, losing every task that had already passed; it now fails itself
	// and the remaining tasks still run.
	defer func() {
		if p := recover(); p != nil {
			res.Passed = false
			res.Reason = fmt.Sprintf("panic: %v", p)
		}
		res.Duration = time.Since(start)
	}()
	var calls atomic.Int64
	stopProgress := r.progress(label, start, &calls)
	defer stopProgress()
	dir, cleanup, err := prepareWorkDir(ctx, t)
	if err != nil {
		res.Reason = "mkdir: " + err.Error()
		return res
	}
	if r.Keep {
		res.Dir = dir
	} else {
		defer cleanup()
	}
	if err := writeFiles(dir, t.Files); err != nil {
		res.Reason = "setup: " + err.Error()
		return res
	}
	before := snapshotState(ctx, dir)
	hiddenGate, hiddenCleanup, err := prepareHidden(t)
	if err != nil {
		res.Reason = "held-out tests: " + err.Error()
		return res
	}
	defer hiddenCleanup()
	ag, err := r.NewAgent(dir, hiddenGate)
	if err != nil {
		res.Reason = "agent: " + err.Error()
		return res
	}
	ag.AutoApprove = true
	timeout := time.Duration(t.Timeout) * time.Second
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	// The budget is per prompt: a session gets it once for each, the way
	// the other harnesses in a comparison are invoked once per prompt.
	// Load refuses a task with no prompt, but one built in memory can
	// have none, and multiplying by zero would hand the run an already
	// expired context.
	steps := t.Steps()
	timeout *= time.Duration(max(len(steps), 1))
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out strings.Builder
	var mu sync.Mutex
	// A session sends its prompts one after another to the same agent;
	// the output the check sees is the answer to the last one.
	for _, prompt := range steps {
		out.Reset()
		err = ag.Run(rctx, prompt, func(e agent.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e.Kind {
			case agent.EvText:
				out.WriteString(e.Text)
			case agent.EvDiscard:
				// A stream that died without a terminal frame is retried
				// from scratch, and its deltas are void. Keeping them
				// scored "half an answerfull answer" as the model's
				// output — a silently wrong eval result.
				out.Reset()
			case agent.EvToolRun:
				res.ToolCalls++
				calls.Add(1)
			case agent.EvTextBlockDone:
				res.Turns++
			}
		})
		if err != nil {
			break
		}
	}
	res.Output = out.String()
	res.Usage = ag.Usage
	// Where the wall time went. A harness change aimed at speed has to
	// know whether it is fighting the model or its own tools, and the
	// totals alone never say: janetcode is faster per model call than
	// Claude Code and still slower overall.
	st := ag.Stats()
	res.ModelTime = st.ModelTime
	res.Recoveries, res.WatchdogKills, res.ModelFallbacks = st.Recoveries, st.WatchdogKills, st.Fallbacks
	res.Decisions = DecisionStats{Calls: st.DecisionCalls, Errors: st.DecisionErrors, Acted: st.DecisionActed, Time: st.DecisionTime, CostUSD: st.DecisionCost}
	for _, t := range st.Tools {
		res.ToolTime += t.Time
	}
	res.Features = ag.Trajectory()
	if ag.Log != nil {
		res.Events = ag.Log.Events()
		if len(res.Events) > 0 {
			res.Features = agent.FoldTrajectory(res.Events)
			res.Compaction = FoldCompaction(res.Events)
			res.Nudges = FoldNudges(res.Events)
			if t.Session() {
				res.Steps = FoldSteps(res.Events)
			}
		}
	}
	res.CostUSD, res.CostKnown = ag.Usage.Cost(ag.Price)
	res.Mismatches, res.Mismatch = st.Mismatches, st.Mismatch
	if !st.Served.Empty() {
		served := st.Served
		res.Served = &served
	}
	if err != nil {
		// What ended the run decides whether the model failed it. A
		// provider refusal, a network error or a dead stream is outside
		// the model; so is a run that timed out before any answer came
		// back, since nothing the model did can have used the budget.
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			res.Reason = fmt.Sprintf("timed out after %s", timeout)
			res.Infra = st.ModelCalls > 0 && st.ModelCalls == st.ModelErrors
		} else {
			res.Reason = "run: " + err.Error()
			res.Infra = llm.Infrastructure(err)
		}
		return res
	}
	if t.MaxToolCalls > 0 && res.ToolCalls > t.MaxToolCalls && !r.IgnoreToolLimit {
		res.Reason = fmt.Sprintf("used %d tool calls, limit %d", res.ToolCalls, t.MaxToolCalls)
		return res
	}
	outPath := filepath.Join(dir, ".eval-output.txt")
	if err := os.WriteFile(outPath, []byte(res.Output), 0o644); err != nil {
		res.Reason = "write output: " + err.Error()
		return res
	}
	if err := restoreLocked(dir, t); err != nil {
		res.Reason = "restore locked files: " + err.Error()
		return res
	}
	var cerr error
	res.CheckOut, cerr = runCheck(ctx, t, dir, outPath, hiddenGate)
	res.Polluted = pollution(ctx, dir, before, t.AllowRefs)
	if cerr != nil {
		res.Reason = "check failed: " + cerr.Error()
		if res.CheckOut != "" {
			res.Reason += ": " + res.CheckOut
		}
		if len(res.Polluted) > 0 {
			res.Reason = strings.Join(res.Polluted, "; ") + " · " + res.Reason
		}
		r.probe(ctx, ag, &res)
		return res
	}
	if len(res.Polluted) > 0 {
		res.Reason = strings.Join(res.Polluted, "; ")
		r.probe(ctx, ag, &res)
		return res
	}
	res.Passed = true
	r.probe(ctx, ag, &res)
	return res
}

// probe runs the fact-survival probes when asked and there is something
// compacted to ask about. It runs after the check so a slow probe cannot
// fail the task.
func (r *Runner) probe(ctx context.Context, ag *agent.Agent, res *Result) {
	if r.Facts <= 0 || len(res.Events) == 0 || res.Compaction.Count == 0 {
		return
	}
	probes := MineFacts(res.Events, r.Facts)
	if len(probes) == 0 {
		return
	}
	fp := ProbeFacts(ctx, ag, probes, r.FactsBlind)
	res.Facts = &fp
	r.logf("  facts %s: %d/%d recalled, %d unknown, %d fetched", res.Name, fp.Correct, fp.Asked, fp.Unknown, fp.Fetched)
}

// Report prints a table and totals; it returns how many passed.
func Report(w io.Writer, results []Result) (passed int) {
	s := Summarize(results)
	if s.Runs > 1 {
		fmt.Fprintf(w, "%-5s %-28s %8s %6s %10s\n", "pass", "task", "time", "tools", "cost")
		for _, tr := range s.ByTask {
			var dur time.Duration
			tools, cost, known := 0, 0.0, true
			var reasons []string
			for _, r := range results {
				if r.Name != tr.Name {
					continue
				}
				dur += r.Duration
				tools += r.ToolCalls
				cost += r.CostUSD
				known = known && r.CostKnown
				switch {
				case r.Mismatches > 0:
					reasons = append(reasons, "MISMATCH, not scored: "+r.Mismatch)
				case r.Infra:
					reasons = append(reasons, "INFRA, not scored: "+textfmt.FirstLine(r.Reason, 0))
				case !r.Passed:
					reasons = append(reasons, textfmt.FirstLine(r.Reason, 0))
				}
			}
			costStr := "?"
			if known {
				costStr = llm.FormatUSDPrecise(cost)
			}
			fmt.Fprintf(w, "%d/%-3d %-28s %8s %6d %10s\n", tr.Passed, tr.Runs, textfmt.Clip(tr.Name, 28), (dur / time.Duration(tr.Runs)).Round(100*time.Millisecond), tools, costStr)
			for _, why := range reasons {
				fmt.Fprintf(w, "      %s\n", textfmt.Clip(why, 100))
			}
		}
		fmt.Fprintf(w, "\n%s\n%s\n", summaryLine(s), costLegend)
		return s.Passed
	}
	defer fmt.Fprintf(w, "context = tokens the model read · cmp = compactions (times the history was summarised) · %s\n", costLegend)
	fmt.Fprintf(w, "%-4s %-28s %8s %6s %8s %4s %10s\n", "", "task", "time", "tools", "context", "cmp", "cost")
	for _, r := range results {
		fmt.Fprintf(w, "%-4s %-28s %8s %6d %8s %4d %10s\n", r.Outcome(), textfmt.Clip(r.Name, 28), r.Duration.Round(100*time.Millisecond), r.ToolCalls, textfmt.Tokens(contextTokens(r.Usage)), r.Compaction.Count, costString(r))
		switch {
		case r.Mismatches > 0:
			fmt.Fprintf(w, "     %s\n", textfmt.Clip(r.Mismatch, 140))
		case !r.Passed:
			fmt.Fprintf(w, "     %s\n", textfmt.Clip(textfmt.FirstLine(r.Reason, 0), 100))
		}
	}
	fmt.Fprintf(w, "\n%s\n", summaryLine(s))
	return s.Passed
}

// costLegend explains a "?" in a report's cost column.
const costLegend = "cost ? = model has no price, so the run could not be costed"

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

// contextTokens is what the model read over a run: uncached input plus
// cache reads plus cache writes. It is the number that compaction and
// pruning exist to hold down, so it is the one the report leads with.
// contextTokens is every token the model read, cached or not. It measures
// how much conversation is moving, which is what a context-management
// change is trying to affect.
//
// It is deliberately not a proxy for the bill. A cache read costs roughly a
// tenth of a fresh input token, so a harness that takes more turns re-reads
// the same cached prefix more often and this number rises far faster than
// the money does: a 48% cost increase once showed up here as 106%, and was
// chased as a regression on that basis. Compare costs with costs; this is
// for asking how much text is in play.
func contextTokens(u llm.Usage) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// freshTokens is the part of the context that was not served from cache —
// the expensive part, and the one a prefix that keeps breaking inflates.
func freshTokens(u llm.Usage) int {
	return u.InputTokens + u.CacheWriteTokens
}

func costString(r Result) string {
	if !r.CostKnown {
		return "?"
	}
	return llm.FormatUSDPrecise(r.CostUSD)
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}
