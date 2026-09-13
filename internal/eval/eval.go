// Package eval runs small end-to-end tasks against a real provider and
// reports pass/fail, cost and tool-call counts. It is the regression test
// for the agent's behaviour, as opposed to its code: a prompt change that
// makes the model stop reading files before editing shows up here.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xmonader/janetcode/internal/tool"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// Check is a shell command run in the work directory afterwards; exit
	// 0 passes. EVAL_OUTPUT names a file holding the model's final text.
	Check string `json:"check"`
	// Timeout in seconds for the model run; zero means 180.
	Timeout int `json:"timeout,omitempty"`
	// MaxToolCalls fails the task when exceeded; zero means no limit.
	MaxToolCalls int `json:"max_tool_calls,omitempty"`
	// Tags let a run select a subset.
	Tags []string `json:"tags,omitempty"`
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
		if seen[t.Name] {
			return nil, fmt.Errorf("%s: duplicate task name %q", f, t.Name)
		}
		seen[t.Name] = true
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
	// Events is the append-only trajectory log when the agent had a Log.
	Events []session.Event `json:"events,omitempty"`
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
	// NewAgent builds an agent whose tools are rooted at dir.
	NewAgent func(dir string) (*agent.Agent, error)
	// Keep leaves work directories on disk and records them in Result.Dir.
	Keep bool
	// Log receives one line per event; nil is quiet.
	Log io.Writer
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
				sem <- struct{}{}
				defer func() { <-sem }()
				res := r.one(ctx, t)
				res.Run = run
				results[idx] = res
				label := t.Name
				if runs > 1 {
					label = fmt.Sprintf("%s #%d", t.Name, run+1)
				}
				r.logf("%s %s (%s, %d tool calls, %s)", mark(res.Passed), label, res.Duration.Round(100*time.Millisecond), res.ToolCalls, costString(res))
				if !res.Passed {
					r.logf("    %s", textfmt.FirstLine(res.Reason, 0))
				}
			}(i*runs+run, run, t)
		}
	}
	wg.Wait()
	return results
}

func (r *Runner) one(ctx context.Context, t Task) (res Result) {
	res = Result{Name: t.Name}
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()
	dir, err := os.MkdirTemp("", "janetcode-eval-"+sanitize(t.Name)+"-")
	if err != nil {
		res.Reason = "mkdir: " + err.Error()
		return res
	}
	if r.Keep {
		res.Dir = dir
	} else {
		defer os.RemoveAll(dir)
	}
	for name, content := range t.Files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		// err is declared here, not in the if: an err scoped to the if
		// leaves the check below reading the outer err, which is the nil
		// from MkdirTemp, and the failure goes unreported.
		err := os.MkdirAll(filepath.Dir(p), 0o755)
		if err == nil {
			err = os.WriteFile(p, []byte(content), 0o644)
		}
		if err != nil {
			res.Reason = "setup: " + err.Error()
			return res
		}
	}
	if _, err := exec.LookPath("git"); err == nil {
		_ = exec.Command("git", "-C", dir, "init", "-q").Run()
	}
	ag, err := r.NewAgent(dir)
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
	timeout *= time.Duration(len(t.Steps()))
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out strings.Builder
	var mu sync.Mutex
	// A session sends its prompts one after another to the same agent;
	// the output the check sees is the answer to the last one.
	for _, prompt := range t.Steps() {
		out.Reset()
		err = ag.Run(rctx, prompt, func(e agent.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e.Kind {
			case agent.EvText:
				out.WriteString(e.Text)
			case agent.EvToolRun:
				res.ToolCalls++
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
	for _, t := range st.Tools {
		res.ToolTime += t.Time
	}
	res.Features = ag.Trajectory()
	if ag.Log != nil {
		res.Events = ag.Log.Events()
		if len(res.Events) > 0 {
			res.Features = agent.FoldTrajectory(res.Events)
			res.Compaction = FoldCompaction(res.Events)
			if t.Session() {
				res.Steps = FoldSteps(res.Events)
			}
		}
	}
	res.CostUSD, res.CostKnown = ag.Usage.Cost(ag.Price)
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			res.Reason = fmt.Sprintf("timed out after %s", timeout)
		} else {
			res.Reason = "run: " + err.Error()
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
	cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
	defer ccancel()
	cmd := tool.ShellCommand(cctx, t.Check)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EVAL_OUTPUT="+outPath, "EVAL_DIR="+dir)
	cout, cerr := cmd.CombinedOutput()
	res.CheckOut = strings.TrimSpace(string(cout))
	if cerr != nil {
		res.Reason = "check failed: " + cerr.Error()
		if res.CheckOut != "" {
			res.Reason += ": " + res.CheckOut
		}
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
				if !r.Passed {
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
		fmt.Fprintf(w, "\n%s\n", summaryLine(s))
		return s.Passed
	}
	fmt.Fprintf(w, "%-4s %-28s %8s %6s %8s %4s %10s\n", "", "task", "time", "tools", "context", "cmp", "cost")
	for _, r := range results {
		fmt.Fprintf(w, "%-4s %-28s %8s %6d %8s %4d %10s\n", mark(r.Passed), textfmt.Clip(r.Name, 28), r.Duration.Round(100*time.Millisecond), r.ToolCalls, textfmt.Tokens(contextTokens(r.Usage)), r.Compaction.Count, costString(r))
		if !r.Passed {
			fmt.Fprintf(w, "     %s\n", textfmt.Clip(textfmt.FirstLine(r.Reason, 0), 100))
		}
	}
	fmt.Fprintf(w, "\n%s\n", summaryLine(s))
	return s.Passed
}

// WriteJSON saves bare results; WriteFile records the run's identity too.
func WriteJSON(path string, results []Result) error {
	return WriteFile(path, File{Results: results})
}

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

func mark(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func costString(r Result) string {
	if !r.CostKnown {
		return "?"
	}
	return llm.FormatUSDPrecise(r.CostUSD)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}
