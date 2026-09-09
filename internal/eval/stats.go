package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// File is the on-disk shape of a results file: the run's identity plus
// every result. Older files hold a bare result array.
type File struct {
	Model   string    `json:"model,omitempty"`
	Variant string    `json:"variant,omitempty"`
	When    time.Time `json:"when"`
	Runs    int       `json:"runs"`
	Results []Result  `json:"results"`
}

// WriteFile saves results with their run identity.
func WriteFile(path string, f File) error {
	if f.When.IsZero() {
		f.When = time.Now()
	}
	if f.Runs == 0 {
		f.Runs = runsIn(f.Results)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadFile loads a results file written by WriteFile or WriteJSON.
func ReadFile(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var rs []Result
		if err := json.Unmarshal(data, &rs); err != nil {
			return File{}, fmt.Errorf("%s: %w", path, err)
		}
		return File{Results: rs, Runs: runsIn(rs)}, nil
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.Runs == 0 {
		f.Runs = runsIn(f.Results)
	}
	return f, nil
}

func runsIn(rs []Result) int {
	runs := 1
	for _, r := range rs {
		if r.Run+1 > runs {
			runs = r.Run + 1
		}
	}
	return runs
}

// Summary aggregates results across tasks and repeated runs.
type Summary struct {
	Tasks, Runs   int
	Passed, Total int
	PassRate      float64
	// Lo and Hi bound the pass rate (Wilson 95% interval over all runs).
	Lo, Hi        float64
	CostUSD       float64
	CostKnown     bool
	CostPerSolved float64 // CostUSD / Passed, 0 when nothing passed
	ToolCalls     int
	Duration      time.Duration
	// Usage is the token total over every run; Context is its
	// input+cache part, the size of what the model read.
	Usage   llm.Usage
	Context int
	// ContextPerSolved is Context / Passed, 0 when nothing passed.
	ContextPerSolved int
	// Compactions and SummaryUsage total the context pipeline's work.
	Compactions  int
	SummaryUsage llm.Usage
	Expands      int
	// ByTask is the pass fraction per task name, sorted by name.
	ByTask []TaskRate
}

// TaskRate is one task's outcome across runs.
type TaskRate struct {
	Name         string
	Passed, Runs int
	Rate         float64
}

// Summarize computes the totals and per-task rates.
func Summarize(rs []Result) Summary {
	s := Summary{CostKnown: true}
	byTask := map[string]*TaskRate{}
	for _, r := range rs {
		s.Total++
		if r.Passed {
			s.Passed++
		}
		s.CostUSD += r.CostUSD
		s.CostKnown = s.CostKnown && r.CostKnown
		s.ToolCalls += r.ToolCalls
		s.Duration += r.Duration
		s.Usage.Add(r.Usage)
		s.Compactions += r.Compaction.Count
		s.SummaryUsage.Add(r.Compaction.Summary)
		s.Expands += r.Compaction.Expands
		tr := byTask[r.Name]
		if tr == nil {
			tr = &TaskRate{Name: r.Name}
			byTask[r.Name] = tr
		}
		tr.Runs++
		if r.Passed {
			tr.Passed++
		}
	}
	for _, tr := range byTask {
		tr.Rate = float64(tr.Passed) / float64(tr.Runs)
		s.ByTask = append(s.ByTask, *tr)
	}
	sort.Slice(s.ByTask, func(i, j int) bool { return s.ByTask[i].Name < s.ByTask[j].Name })
	s.Tasks = len(s.ByTask)
	s.Runs = runsIn(rs)
	if s.Total > 0 {
		s.PassRate = float64(s.Passed) / float64(s.Total)
		s.Lo, s.Hi = wilson(s.Passed, s.Total)
	}
	if s.Passed > 0 && s.CostKnown {
		s.CostPerSolved = s.CostUSD / float64(s.Passed)
	}
	s.Context = contextTokens(s.Usage)
	if s.Passed > 0 {
		s.ContextPerSolved = s.Context / s.Passed
	}
	return s
}

// wilson is the 95% score interval for a binomial proportion; unlike the
// normal approximation it behaves at 0/n and n/n.
func wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 0
	}
	const z = 1.96
	p := float64(k) / float64(n)
	nn := float64(n)
	denom := 1 + z*z/nn
	centre := (p + z*z/(2*nn)) / denom
	half := z * math.Sqrt(p*(1-p)/nn+z*z/(4*nn*nn)) / denom
	return math.Max(0, centre-half), math.Min(1, centre+half)
}

// Comparison is a paired comparison of two result sets by task.
type Comparison struct {
	Base, Current Summary
	// Delta is the mean over shared tasks of (current rate − base rate);
	// Lo and Hi bound it (95% paired bootstrap over tasks).
	Delta, Lo, Hi float64
	Shared        int
	Improved      []string // tasks whose rate went up
	Regressed     []string // tasks whose rate went down
	OnlyBase      []string
	OnlyCurrent   []string
	// CostDelta is current − base cost per solved task, when both known.
	CostDelta float64
	CostKnown bool
	// ContextDelta is current − base context tokens per solved task; the
	// ratio is Current.ContextPerSolved / Base.ContextPerSolved.
	ContextDelta int
}

// Compare pairs the two sets by task name. Tasks present in only one set
// are listed but do not enter the delta.
func Compare(base, cur []Result) Comparison {
	c := Comparison{Base: Summarize(base), Current: Summarize(cur)}
	brate := map[string]float64{}
	for _, t := range c.Base.ByTask {
		brate[t.Name] = t.Rate
	}
	crate := map[string]float64{}
	for _, t := range c.Current.ByTask {
		crate[t.Name] = t.Rate
	}
	var deltas []float64
	for _, t := range c.Current.ByTask {
		b, ok := brate[t.Name]
		if !ok {
			c.OnlyCurrent = append(c.OnlyCurrent, t.Name)
			continue
		}
		d := t.Rate - b
		deltas = append(deltas, d)
		switch {
		case d > 0:
			c.Improved = append(c.Improved, t.Name)
		case d < 0:
			c.Regressed = append(c.Regressed, t.Name)
		}
	}
	for _, t := range c.Base.ByTask {
		if _, ok := crate[t.Name]; !ok {
			c.OnlyBase = append(c.OnlyBase, t.Name)
		}
	}
	c.Shared = len(deltas)
	if c.Shared > 0 {
		c.Delta = mean(deltas)
		c.Lo, c.Hi = bootstrapCI(deltas, 4000, 1)
	}
	if c.Base.CostKnown && c.Current.CostKnown && c.Base.Passed > 0 && c.Current.Passed > 0 {
		c.CostKnown = true
		c.CostDelta = c.Current.CostPerSolved - c.Base.CostPerSolved
	}
	if c.Base.Passed > 0 && c.Current.Passed > 0 {
		c.ContextDelta = c.Current.ContextPerSolved - c.Base.ContextPerSolved
	}
	return c
}

// Significant reports whether the interval excludes zero.
func (c Comparison) Significant() bool { return c.Shared > 0 && (c.Lo > 0 || c.Hi < 0) }

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// bootstrapCI resamples the per-task deltas with replacement and returns
// the 2.5th and 97.5th percentiles of the resampled means. The seed makes
// reports reproducible.
func bootstrapCI(deltas []float64, resamples int, seed int64) (lo, hi float64) {
	if len(deltas) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, resamples)
	for i := range means {
		s := 0.0
		for range deltas {
			s += deltas[rng.Intn(len(deltas))]
		}
		means[i] = s / float64(len(deltas))
	}
	sort.Float64s(means)
	return means[int(0.025*float64(resamples))], means[int(0.975*float64(resamples))-1]
}

// WriteComparison prints the paired comparison.
func WriteComparison(w io.Writer, c Comparison) {
	pct := func(x float64) string { return fmt.Sprintf("%+.1f%%", 100*x) }
	fmt.Fprintf(w, "base    %s\ncurrent %s\n", summaryLine(c.Base), summaryLine(c.Current))
	if c.Shared == 0 {
		fmt.Fprintln(w, "no shared tasks to compare")
		return
	}
	verdict := "not significant (the interval includes 0)"
	switch {
	case c.Lo > 0:
		verdict = "significant improvement"
	case c.Hi < 0:
		verdict = "significant regression"
	}
	fmt.Fprintf(w, "\npass-rate delta %s [%s, %s] over %d shared task(s) · %s\n", pct(c.Delta), pct(c.Lo), pct(c.Hi), c.Shared, verdict)
	if c.CostKnown {
		sign := "+"
		if c.CostDelta < 0 {
			sign = "-"
		}
		fmt.Fprintf(w, "cost per solved task %s → %s (%s%s)\n", llm.FormatUSD(c.Base.CostPerSolved), llm.FormatUSD(c.Current.CostPerSolved), sign, llm.FormatUSD(math.Abs(c.CostDelta)))
	}
	if c.Base.Passed > 0 && c.Current.Passed > 0 {
		ratio := float64(c.Current.ContextPerSolved) / math.Max(float64(c.Base.ContextPerSolved), 1)
		fmt.Fprintf(w, "context per solved task %s → %s (×%.2f) · compactions %d → %d\n",
			shortTokens(c.Base.ContextPerSolved), shortTokens(c.Current.ContextPerSolved), ratio, c.Base.Compactions, c.Current.Compactions)
	}
	if len(c.Improved) > 0 {
		fmt.Fprintf(w, "improved:  %s\n", strings.Join(c.Improved, ", "))
	}
	if len(c.Regressed) > 0 {
		fmt.Fprintf(w, "regressed: %s\n", strings.Join(c.Regressed, ", "))
	}
	if len(c.OnlyBase) > 0 {
		fmt.Fprintf(w, "only in base: %s\n", strings.Join(c.OnlyBase, ", "))
	}
	if len(c.OnlyCurrent) > 0 {
		fmt.Fprintf(w, "only in current: %s\n", strings.Join(c.OnlyCurrent, ", "))
	}
}

func summaryLine(s Summary) string {
	cost := "cost unknown"
	if s.CostKnown {
		cost = "cost " + llm.FormatUSD(s.CostUSD)
		if s.Passed > 0 {
			cost += " (" + llm.FormatUSD(s.CostPerSolved) + " per solved)"
		}
	}
	ctx := fmt.Sprintf("context %s (in %s · cache %s · out %s)", shortTokens(s.Context), shortTokens(s.Usage.InputTokens), shortTokens(s.Usage.CacheReadTokens), shortTokens(s.Usage.OutputTokens))
	if s.Passed > 0 {
		ctx += fmt.Sprintf(" · %s per solved", shortTokens(s.ContextPerSolved))
	}
	if s.Compactions > 0 {
		ctx += fmt.Sprintf(" · %d compaction(s), summaries %s in / %s out, %d expand(s)", s.Compactions, shortTokens(contextTokens(s.SummaryUsage)), shortTokens(s.SummaryUsage.OutputTokens), s.Expands)
	}
	return fmt.Sprintf("%d/%d passed (%.0f%%, 95%% CI %.0f–%.0f%%) · %d task(s) × %d run(s) · %d tool calls · %s · %s\n%s",
		s.Passed, s.Total, 100*s.PassRate, 100*s.Lo, 100*s.Hi, s.Tasks, s.Runs, s.ToolCalls, s.Duration.Round(time.Second), cost, ctx)
}
