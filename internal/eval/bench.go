package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Bench harness names and the 01–20 grouping the live runner uses.
const (
	BenchBatchSize = 5
	BenchTaskMin   = 1
	BenchTaskMax   = 20
)

// BenchHarnesses is the comparison set, in report order.
var BenchHarnesses = []string{
	"pi", "janetcode", "qwencode", "hermes-agent", "openclaw", "grok",
}

// OptInt is a metric a harness may omit. Set=false means unavailable, not zero.
type OptInt struct {
	Value int  `json:"value,omitempty"`
	Set   bool `json:"set"`
}

// OptFloat is an optional floating metric (cost).
type OptFloat struct {
	Value float64 `json:"value,omitempty"`
	Set   bool    `json:"set"`
}

// Int returns an optional int that was reported.
func Int(v int) OptInt { return OptInt{Value: v, Set: true} }

// Float returns an optional float that was reported.
func Float(v float64) OptFloat { return OptFloat{Value: v, Set: true} }

// Add sums two optional ints; the result is set if either operand is.
func (a OptInt) Add(b OptInt) OptInt {
	if !a.Set && !b.Set {
		return OptInt{}
	}
	return OptInt{Value: a.Value + b.Value, Set: true}
}

// Add sums two optional floats; the result is set if either operand is.
func (a OptFloat) Add(b OptFloat) OptFloat {
	if !a.Set && !b.Set {
		return OptFloat{}
	}
	return OptFloat{Value: a.Value + b.Value, Set: true}
}

func (o OptInt) String() string {
	if !o.Set {
		return "n/a"
	}
	return strconv.Itoa(o.Value)
}

func (o OptFloat) String() string {
	if !o.Set {
		return "n/a"
	}
	return fmt.Sprintf("%.6f", o.Value)
}

// UsageMetrics is token and call accounting extracted from a harness, never estimated.
type UsageMetrics struct {
	Input      OptInt
	Output     OptInt
	CacheRead  OptInt
	CacheWrite OptInt
	ToolCalls  OptInt
	Turns      OptInt
	CostUSD    OptFloat
}

// HasTokens reports whether any token field was present.
func (u UsageMetrics) HasTokens() bool {
	return u.Input.Set || u.Output.Set || u.CacheRead.Set || u.CacheWrite.Set
}

// Add accumulates another usage blob (per-turn events).
func (u UsageMetrics) Add(o UsageMetrics) UsageMetrics {
	return UsageMetrics{
		Input:      u.Input.Add(o.Input),
		Output:     u.Output.Add(o.Output),
		CacheRead:  u.CacheRead.Add(o.CacheRead),
		CacheWrite: u.CacheWrite.Add(o.CacheWrite),
		ToolCalls:  u.ToolCalls.Add(o.ToolCalls),
		Turns:      u.Turns.Add(o.Turns),
		CostUSD:    u.CostUSD.Add(o.CostUSD),
	}
}

// BenchTaskResult is one task run on one harness.
type BenchTaskResult struct {
	Harness           string         `json:"harness"`
	Task              string         `json:"task"`
	Batch             int            `json:"batch"`
	Passed            bool           `json:"passed"`
	Reason            string         `json:"reason,omitempty"`
	WallTime          time.Duration  `json:"-"`
	WallTimeMS        int64          `json:"wall_time_ms"`
	InputTokens       OptInt         `json:"input_tokens"`
	OutputTokens      OptInt         `json:"output_tokens"`
	CacheReadTokens   OptInt         `json:"cache_read_tokens"`
	CacheWriteTokens  OptInt         `json:"cache_write_tokens"`
	ToolCalls         OptInt         `json:"tool_calls"`
	Turns             OptInt         `json:"turns"`
	CostUSD           OptFloat       `json:"cost_usd"`
	Command           string         `json:"command,omitempty"`
	ExitCode          int            `json:"exit_code,omitempty"`
	StderrExcerpt     string         `json:"stderr_excerpt,omitempty"`
	Unavailable       bool           `json:"unavailable,omitempty"`
	UnavailableReason string         `json:"unavailable_reason,omitempty"`
	Extra             map[string]any `json:"extra,omitempty"`
	// Steps is the per-prompt usage of a session task, as the harness
	// reported it for each invocation.
	Steps []BenchStep `json:"steps,omitempty"`
}

// BenchStep is one prompt of a session on one harness.
type BenchStep struct {
	Input      OptInt `json:"input_tokens"`
	CacheRead  OptInt `json:"cache_read_tokens"`
	CacheWrite OptInt `json:"cache_write_tokens"`
	Output     OptInt `json:"output_tokens"`
	Turns      OptInt `json:"turns"`
	WallTimeMS int64  `json:"wall_time_ms"`
}

// Context is what the model read in the step: input plus cache tokens.
func (s BenchStep) Context() OptInt { return s.Input.Add(s.CacheRead).Add(s.CacheWrite) }

// ApplyUsage copies reported usage onto the result.
func (r *BenchTaskResult) ApplyUsage(u UsageMetrics) {
	r.InputTokens = u.Input
	r.OutputTokens = u.Output
	r.CacheReadTokens = u.CacheRead
	r.CacheWriteTokens = u.CacheWrite
	r.ToolCalls = u.ToolCalls
	r.Turns = u.Turns
	r.CostUSD = u.CostUSD
}

// SetWall records wall time.
func (r *BenchTaskResult) SetWall(d time.Duration) {
	r.WallTime = d
	r.WallTimeMS = d.Milliseconds()
}

// BenchTotals is the per-harness rollup used in the comparative report.
type BenchTotals struct {
	Harness     string
	Tasks       int
	Passed      int
	Failed      int
	Unavailable int
	WallTime    time.Duration
	Input       OptInt
	Output      OptInt
	CacheRead   OptInt
	CacheWrite  OptInt
	ToolCalls   OptInt
	Turns       OptInt
	CostUSD     OptFloat
}

// ReportMeta is the header of the comparative markdown report.
type ReportMeta struct {
	Model    string
	Endpoint string
	When     time.Time
	Note     string
	// Tasks describes the eval set; empty means the 01–20 four-batch suite.
	Tasks string
}

// TaskIndex parses the leading NN- from an eval task name. Zero if none.
func TaskIndex(name string) int {
	i := strings.IndexByte(name, '-')
	if i <= 0 || i > 3 {
		return 0
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0
	}
	return n
}

// Filter01to20 keeps evals 01 through 20, sorted by numeric prefix.
func Filter01to20(tasks []Task) []Task {
	var out []Task
	for _, t := range tasks {
		n := TaskIndex(t.Name)
		if n >= BenchTaskMin && n <= BenchTaskMax {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return TaskIndex(out[i].Name) < TaskIndex(out[j].Name) })
	return out
}

// BatchesOfFive groups tasks into consecutive batches of BenchBatchSize
// (01–05, 06–10, 11–15, 16–20 when given the twenty named evals in order).
func BatchesOfFive(tasks []Task) [][]Task {
	if len(tasks) == 0 {
		return nil
	}
	var batches [][]Task
	for i := 0; i < len(tasks); i += BenchBatchSize {
		end := i + BenchBatchSize
		if end > len(tasks) {
			end = len(tasks)
		}
		batch := make([]Task, end-i)
		copy(batch, tasks[i:end])
		batches = append(batches, batch)
	}
	return batches
}

// FromJanetResult maps a native janetcode eval result onto a bench row.
func FromJanetResult(harness string, batch int, r Result) BenchTaskResult {
	out := BenchTaskResult{
		Harness:          harness,
		Task:             r.Name,
		Batch:            batch,
		Passed:           r.Passed,
		Reason:           r.Reason,
		InputTokens:      Int(r.Usage.InputTokens),
		OutputTokens:     Int(r.Usage.OutputTokens),
		CacheReadTokens:  Int(r.Usage.CacheReadTokens),
		CacheWriteTokens: Int(r.Usage.CacheWriteTokens),
		ToolCalls:        Int(r.ToolCalls),
		Turns:            Int(r.Turns),
	}
	out.SetWall(r.Duration)
	if r.CostKnown {
		out.CostUSD = Float(r.CostUSD)
	}
	for _, st := range r.Steps {
		out.Steps = append(out.Steps, BenchStep{
			Input: Int(st.Usage.InputTokens), CacheRead: Int(st.Usage.CacheReadTokens), CacheWrite: Int(st.Usage.CacheWriteTokens),
			Output: Int(st.Usage.OutputTokens), Turns: Int(st.Turns),
		})
	}
	return out
}

// UnavailableResults writes one row per task so a harness that cannot start
// is still represented for every name in 01–20.
func UnavailableResults(harness, reason, command, stderr string, tasks []Task) []BenchTaskResult {
	out := make([]BenchTaskResult, 0, len(tasks))
	for i, t := range tasks {
		out = append(out, BenchTaskResult{
			Harness:           harness,
			Task:              t.Name,
			Batch:             i/BenchBatchSize + 1,
			Passed:            false,
			Reason:            reason,
			Unavailable:       true,
			UnavailableReason: reason,
			Command:           command,
			StderrExcerpt:     trimExcerpt(stderr, 2000),
		})
	}
	return out
}

// AggregateHarness sums reported fields. Unreported token fields stay n/a.
func AggregateHarness(results []BenchTaskResult) BenchTotals {
	var t BenchTotals
	if len(results) > 0 {
		t.Harness = results[0].Harness
	}
	for _, r := range results {
		t.Tasks++
		if r.Unavailable {
			t.Unavailable++
		} else if r.Passed {
			t.Passed++
		} else {
			t.Failed++
		}
		t.WallTime += r.WallTime
		if r.WallTime == 0 && r.WallTimeMS > 0 {
			t.WallTime += time.Duration(r.WallTimeMS) * time.Millisecond
		}
		t.Input = t.Input.Add(r.InputTokens)
		t.Output = t.Output.Add(r.OutputTokens)
		t.CacheRead = t.CacheRead.Add(r.CacheReadTokens)
		t.CacheWrite = t.CacheWrite.Add(r.CacheWriteTokens)
		t.ToolCalls = t.ToolCalls.Add(r.ToolCalls)
		t.Turns = t.Turns.Add(r.Turns)
		t.CostUSD = t.CostUSD.Add(r.CostUSD)
	}
	return t
}

// RenderComparativeReport writes markdown naming input / cache / output tokens and wall time.
func RenderComparativeReport(byHarness map[string][]BenchTaskResult, meta ReportMeta) string {
	var b strings.Builder
	when := meta.When
	if when.IsZero() {
		when = time.Now()
	}
	fmt.Fprintf(&b, "# Harness benchmark report\n\n")
	fmt.Fprintf(&b, "- Model: `%s`\n", meta.Model)
	fmt.Fprintf(&b, "- Endpoint: `%s`\n", meta.Endpoint)
	tasksLine := meta.Tasks
	if tasksLine == "" {
		tasksLine = "01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)"
	}
	fmt.Fprintf(&b, "- Tasks: %s\n", tasksLine)
	fmt.Fprintf(&b, "- When: %s\n", when.Format(time.RFC3339))
	if meta.Note != "" {
		fmt.Fprintf(&b, "- Note: %s\n", meta.Note)
	}
	fmt.Fprintf(&b, "\nToken columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.\n")

	names := harnessOrder(byHarness)
	fmt.Fprintf(&b, "\n## Per-harness totals\n\n")
	fmt.Fprintf(&b, "| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, h := range names {
		tot := AggregateHarness(byHarness[h])
		fmt.Fprintf(&b, "| %s | %d/%d | %d | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			h, tot.Passed, tot.Tasks-tot.Unavailable, tot.Unavailable,
			fmtDuration(tot.WallTime),
			tot.Input, tot.CacheRead, tot.CacheWrite, tot.Output,
			tot.ToolCalls, tot.Turns, tot.CostUSD)
	}

	taskNames := unionTaskNames(byHarness)
	fmt.Fprintf(&b, "\n## Per-task wall time\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string {
		if r.Unavailable {
			return "n/a"
		}
		d := r.WallTime
		if d == 0 && r.WallTimeMS > 0 {
			d = time.Duration(r.WallTimeMS) * time.Millisecond
		}
		return fmtDuration(d)
	})

	fmt.Fprintf(&b, "\n## Per-task input tokens\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string { return r.InputTokens.String() })
	fmt.Fprintf(&b, "\n## Per-task cache read tokens\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string { return r.CacheReadTokens.String() })
	fmt.Fprintf(&b, "\n## Per-task cache write tokens\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string { return r.CacheWriteTokens.String() })
	fmt.Fprintf(&b, "\n## Per-task output tokens\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string { return r.OutputTokens.String() })
	fmt.Fprintf(&b, "\n## Per-task pass/fail\n\n")
	writeTaskTable(&b, names, taskNames, byHarness, func(r BenchTaskResult) string {
		if r.Unavailable {
			return "unavailable"
		}
		if r.Passed {
			return "PASS"
		}
		return "FAIL"
	})

	writeSessionGrowth(&b, names, taskNames, byHarness)

	fmt.Fprintf(&b, "\n## Failures and unavailable harnesses\n\n")
	any := false
	for _, h := range names {
		for _, r := range byHarness[h] {
			if r.Passed && !r.Unavailable {
				continue
			}
			any = true
			why := r.Reason
			if r.Unavailable && r.UnavailableReason != "" {
				why = r.UnavailableReason
			}
			fmt.Fprintf(&b, "- **%s / %s** (batch %d): %s\n", h, r.Task, r.Batch, firstLine(why))
			if r.Command != "" {
				fmt.Fprintf(&b, "  - command: `%s`\n", r.Command)
			}
			if r.StderrExcerpt != "" {
				fmt.Fprintf(&b, "  - stderr: %s\n", firstLine(r.StderrExcerpt))
			}
		}
	}
	if !any {
		fmt.Fprintf(&b, "None.\n")
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}

// writeSessionGrowth renders, for every session task, what each harness
// read per prompt: the input plus cache tokens of that step's calls. Read
// left to right it is the context-growth curve — a harness that resends
// the whole history grows every column; one that prunes or summarises
// flattens out. Only tasks with per-step data get a table.
func writeSessionGrowth(b *strings.Builder, harnesses, tasks []string, byHarness map[string][]BenchTaskResult) {
	idx := indexResults(byHarness)
	wrote := false
	for _, task := range tasks {
		steps := 0
		for _, h := range harnesses {
			if r, ok := idx[h+"/"+task]; ok && len(r.Steps) > steps {
				steps = len(r.Steps)
			}
		}
		if steps < 2 {
			continue
		}
		if !wrote {
			fmt.Fprintf(b, "\n## Session context growth\n\nPer prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.\n")
			wrote = true
		}
		fmt.Fprintf(b, "\n### %s\n\n| harness |", task)
		for i := 1; i <= steps; i++ {
			fmt.Fprintf(b, " p%d |", i)
		}
		fmt.Fprintf(b, " total |\n|---|")
		for i := 0; i <= steps; i++ {
			fmt.Fprintf(b, "---:|")
		}
		fmt.Fprintf(b, "\n")
		for _, h := range harnesses {
			r, ok := idx[h+"/"+task]
			if !ok || r.Unavailable {
				continue
			}
			fmt.Fprintf(b, "| %s |", h)
			var total OptInt
			for i := 0; i < steps; i++ {
				if i < len(r.Steps) {
					c := r.Steps[i].Context()
					total = total.Add(c)
					fmt.Fprintf(b, " %s |", c)
				} else {
					fmt.Fprintf(b, " n/a |")
				}
			}
			fmt.Fprintf(b, " %s |\n", total)
		}
	}
}

func writeTaskTable(b *strings.Builder, harnesses, tasks []string, byHarness map[string][]BenchTaskResult, cell func(BenchTaskResult) string) {
	fmt.Fprintf(b, "| task |")
	for _, h := range harnesses {
		fmt.Fprintf(b, " %s |", h)
	}
	fmt.Fprintf(b, "\n|---|")
	for range harnesses {
		fmt.Fprintf(b, "---:|")
	}
	fmt.Fprintf(b, "\n")
	index := indexResults(byHarness)
	for _, name := range tasks {
		fmt.Fprintf(b, "| %s |", name)
		for _, h := range harnesses {
			r, ok := index[h+"/"+name]
			if !ok {
				fmt.Fprintf(b, " n/a |")
				continue
			}
			fmt.Fprintf(b, " %s |", cell(r))
		}
		fmt.Fprintf(b, "\n")
	}
}

func indexResults(byHarness map[string][]BenchTaskResult) map[string]BenchTaskResult {
	m := map[string]BenchTaskResult{}
	for h, rs := range byHarness {
		for _, r := range rs {
			m[h+"/"+r.Task] = r
		}
	}
	return m
}

func harnessOrder(byHarness map[string][]BenchTaskResult) []string {
	var names []string
	seen := map[string]bool{}
	for _, h := range BenchHarnesses {
		if _, ok := byHarness[h]; ok {
			names = append(names, h)
			seen[h] = true
		}
	}
	var extra []string
	for h := range byHarness {
		if !seen[h] {
			extra = append(extra, h)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}

func unionTaskNames(byHarness map[string][]BenchTaskResult) []string {
	seen := map[string]bool{}
	var names []string
	for _, h := range harnessOrder(byHarness) {
		for _, r := range byHarness[h] {
			if !seen[r.Task] {
				seen[r.Task] = true
				names = append(names, r.Task)
			}
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return TaskIndex(names[i]) < TaskIndex(names[j]) })
	return names
}

func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Millisecond)
	if d >= time.Second {
		return d.Truncate(10 * time.Millisecond).String()
	}
	return d.String()
}

func trimExcerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// MergeBenchResults overlays neu onto old by task name. Tasks only in old
// are kept; a task in neu replaces the old row. Order is old's order, then
// names that appear only in neu.
func MergeBenchResults(old, neu []BenchTaskResult) []BenchTaskResult {
	by := map[string]BenchTaskResult{}
	var order []string
	for _, r := range old {
		if _, ok := by[r.Task]; !ok {
			order = append(order, r.Task)
		}
		by[r.Task] = r
	}
	for _, r := range neu {
		if _, ok := by[r.Task]; !ok {
			order = append(order, r.Task)
		}
		by[r.Task] = r
	}
	out := make([]BenchTaskResult, 0, len(order))
	for _, name := range order {
		out = append(out, by[name])
	}
	return out
}

// WriteBenchJSON writes one harness's raw results.
func WriteBenchJSON(path string, results []BenchTaskResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadBenchJSON loads a harness results file.
func ReadBenchJSON(path string) ([]BenchTaskResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs []BenchTaskResult
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	for i := range rs {
		if rs[i].WallTime == 0 && rs[i].WallTimeMS > 0 {
			rs[i].WallTime = time.Duration(rs[i].WallTimeMS) * time.Millisecond
		}
	}
	return rs, nil
}
