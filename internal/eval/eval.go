// SPDX-License-Identifier: MIT
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
)

// Task is one eval case, stored as JSON.
type Task struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
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
		if strings.TrimSpace(t.Prompt) == "" || strings.TrimSpace(t.Check) == "" {
			return nil, fmt.Errorf("%s: prompt and check are required", f)
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
	Name      string        `json:"name"`
	Run       int           `json:"run"` // 0-based repetition index
	Passed    bool          `json:"passed"`
	Reason    string        `json:"reason,omitempty"`
	Duration  time.Duration `json:"duration"`
	ToolCalls int           `json:"tool_calls"`
	Turns     int           `json:"turns"` // model calls
	Usage     llm.Usage     `json:"usage"`
	CostUSD   float64       `json:"cost_usd"`
	CostKnown bool          `json:"cost_known"`
	Output    string        `json:"output"`
	CheckOut  string        `json:"check_output,omitempty"`
	Dir       string        `json:"dir,omitempty"`
	// Features is the agent's trajectory at the end of the run, the
	// training data for the success predictor (agent.Train).
	Features map[string]float64 `json:"features,omitempty"`
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
					r.logf("    %s", firstLine(res.Reason))
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
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
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
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out strings.Builder
	var mu sync.Mutex
	err = ag.Run(rctx, t.Prompt, func(e agent.Event) {
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
	res.Output = out.String()
	res.Usage = ag.Usage
	res.Features = ag.Trajectory()
	res.CostUSD, res.CostKnown = ag.Usage.Cost(ag.Price)
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			res.Reason = fmt.Sprintf("timed out after %s", timeout)
		} else {
			res.Reason = "run: " + err.Error()
		}
		return res
	}
	if t.MaxToolCalls > 0 && res.ToolCalls > t.MaxToolCalls {
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
		return res
	}
	res.Passed = true
	return res
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
					reasons = append(reasons, firstLine(r.Reason))
				}
			}
			costStr := "?"
			if known {
				costStr = llm.FormatUSD(cost)
			}
			fmt.Fprintf(w, "%d/%-3d %-28s %8s %6d %10s\n", tr.Passed, tr.Runs, trunc(tr.Name, 28), (dur / time.Duration(tr.Runs)).Round(100*time.Millisecond), tools, costStr)
			for _, why := range reasons {
				fmt.Fprintf(w, "      %s\n", trunc(why, 100))
			}
		}
		fmt.Fprintf(w, "\n%s\n", summaryLine(s))
		return s.Passed
	}
	fmt.Fprintf(w, "%-4s %-28s %8s %6s %10s\n", "", "task", "time", "tools", "cost")
	for _, r := range results {
		fmt.Fprintf(w, "%-4s %-28s %8s %6d %10s\n", mark(r.Passed), trunc(r.Name, 28), r.Duration.Round(100*time.Millisecond), r.ToolCalls, costString(r))
		if !r.Passed {
			fmt.Fprintf(w, "     %s\n", trunc(firstLine(r.Reason), 100))
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
	return llm.FormatUSD(r.CostUSD)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
