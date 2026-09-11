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
	"github.com/xmonader/janetcode/internal/session"
)

// File is the on-disk shape of a results file: the run's identity plus
// every result. Older files hold a bare result array.
type File struct {
	Model   string    `json:"model,omitempty"`
	Variant string    `json:"variant,omitempty"`
	When    time.Time `json:"when"`
	Runs    int       `json:"runs"`
	// Prompt records a replaced or extended system prompt. Two runs that
	// differ only in their prompt are otherwise indistinguishable in the
	// file, which is the one comparison a prompt experiment needs.
	Prompt  *PromptRecord `json:"prompt,omitempty"`
	Results []Result      `json:"results"`
}

// PromptRecord identifies the prompt a run used: which file replaced the
// base, what was appended, and a digest of the whole thing as sent, so a
// file that has since changed cannot be mistaken for the one measured.
type PromptRecord struct {
	BaseFile string `json:"base_file,omitempty"`
	Append   string `json:"append,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
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
	// FactsAsked/FactsCorrect/FactsUnknown total the fact-survival probes.
	FactsAsked, FactsCorrect, FactsUnknown, FactsFetched, FactsGrounded int
	FactsInArchive, FactsRetrieved                                      int
	// ModelTime and ToolTime say where the wall time went.
	ModelTime, ToolTime time.Duration
	// Recoveries, WatchdogKills and ModelFallbacks total the harness's
	// repair work across the runs: prose read back as tool calls, streams
	// ended for going quiet, and calls that only worked on another model.
	Recoveries, WatchdogKills, ModelFallbacks int
	// ByTask is the pass fraction per task name, sorted by name.
	ByTask []TaskRate
	// ByTool is how often each tool was actually reached for. A tool the
	// model never calls is not a feature, however good it is, and that is
	// only visible across runs: one session proves nothing either way.
	ByTool []ToolUse
}

// ToolUse is one tool's adoption across the runs of a set.
type ToolUse struct {
	Name string
	// Runs is how many runs called it at least once, out of Total.
	Runs, Total int
	// Calls is how many times in all. Nested is the subset made from
	// inside a script, whose output never reached the model.
	Calls, Nested int
}

// Rate is the fraction of runs that reached for the tool.
func (t ToolUse) Rate() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Runs) / float64(t.Total)
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
		s.ModelTime += r.ModelTime
		s.ToolTime += r.ToolTime
		s.Recoveries += r.Recoveries
		s.WatchdogKills += r.WatchdogKills
		s.ModelFallbacks += r.ModelFallbacks
		s.Usage.Add(r.Usage)
		s.Compactions += r.Compaction.Count
		s.SummaryUsage.Add(r.Compaction.Summary)
		s.Expands += r.Compaction.Expands
		if r.Facts != nil {
			s.FactsAsked += r.Facts.Asked
			s.FactsCorrect += r.Facts.Correct
			s.FactsUnknown += r.Facts.Unknown
			s.FactsFetched += r.Facts.Fetched
			s.FactsGrounded += r.Facts.Grounded
			s.FactsInArchive += r.Facts.InArchive
			s.FactsRetrieved += r.Facts.Retrieved
		}
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
	s.ByTool = toolUse(rs)
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

// toolUse counts, per tool, how many runs reached for it and how often.
// It reads the runs' event logs, so it sees the calls a script made on the
// model's behalf as well as the ones the model made itself.
func toolUse(rs []Result) []ToolUse {
	byName := map[string]*ToolUse{}
	for _, r := range rs {
		seen := map[string]bool{}
		for _, e := range r.Events {
			if e.Type != session.TypeToolCall || e.Name == "" {
				continue
			}
			u := byName[e.Name]
			if u == nil {
				u = &ToolUse{Name: e.Name}
				byName[e.Name] = u
			}
			u.Calls++
			if strings.HasPrefix(e.CallID, "script-") {
				u.Nested++
			}
			if !seen[e.Name] {
				seen[e.Name] = true
				u.Runs++
			}
		}
	}
	out := make([]ToolUse, 0, len(byName))
	for _, u := range byName {
		u.Total = len(rs)
		out = append(out, *u)
	}
	// Most-used first: the question is which tools carry the work.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// WriteToolUse reports adoption, one line per tool, and names the tools
// that were available and never called at all.
func WriteToolUse(w io.Writer, s Summary, available []string) {
	if len(s.ByTool) == 0 {
		return
	}
	called := map[string]bool{}
	fmt.Fprintf(w, "\ntool adoption over %d run(s):\n", s.Total)
	for _, u := range s.ByTool {
		called[u.Name] = true
		nested := ""
		if u.Nested > 0 {
			nested = fmt.Sprintf(", %d from inside a script", u.Nested)
		}
		fmt.Fprintf(w, "  %-16s %2d/%-2d runs (%.0f%%) · %d call(s)%s\n", u.Name, u.Runs, u.Total, 100*u.Rate(), u.Calls, nested)
	}
	var never []string
	for _, n := range available {
		if !called[n] {
			never = append(never, n)
		}
	}
	if len(never) > 0 {
		sort.Strings(never)
		fmt.Fprintf(w, "  never called: %s\n", strings.Join(never, ", "))
	}
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

	// Cost and context are what a change to the context pipeline is
	// usually claimed on, and a point estimate of either is not a result:
	// run-to-run spread on this task set is wide enough to produce a
	// confident double-digit "improvement" from noise alone. So they get
	// the same paired bootstrap over tasks that the pass rate gets, and
	// the ratio — the form the claim is actually made in — gets its own
	// interval rather than being computed from the totals.
	CostRatio, CostRatioLo, CostRatioHi          float64
	ContextRatio, ContextRatioLo, ContextRatioHi float64
	// Output tokens are the wall-clock term: generation dominates a turn's
	// latency, so a change aimed at speed is a change to this number.
	OutputRatio, OutputRatioLo, OutputRatioHi float64
	// Paired reports whether there were enough shared tasks with usable
	// numbers to bootstrap the ratios at all.
	Paired bool
}

// CostSignificant reports whether the cost ratio's interval excludes 1,
// i.e. whether the difference survived the noise.
func (c Comparison) CostSignificant() bool {
	return c.Paired && hasInterval(c.CostRatioLo, c.CostRatioHi) && (c.CostRatioLo > 1 || c.CostRatioHi < 1)
}

// hasInterval reports whether a bootstrap actually produced bounds. Too
// few tasks leaves them at zero, and zero must never read as "below one".
func hasInterval(lo, hi float64) bool { return lo > 0 && hi > 0 }

// ContextSignificant reports whether the context ratio's interval
// excludes 1.
func (c Comparison) ContextSignificant() bool {
	return c.Paired && hasInterval(c.ContextRatioLo, c.ContextRatioHi) && (c.ContextRatioLo > 1 || c.ContextRatioHi < 1)
}

// pair is one task's mean cost and context in each set, for the paired
// bootstrap. Means are per run of that task, passed or not: a change that
// makes runs cheaper by failing sooner is a change worth seeing, and the
// pass-rate delta printed beside it is what says whether that happened.
type pair struct {
	base, cur       float64
	baseCtx, curCtx float64
	baseOut, curOut float64
}

// ratioCI bootstraps the ratio of summed current to summed base over the
// shared tasks. Resampling whole tasks, not runs, is what makes it paired:
// a task that is expensive in both sets cannot move the ratio on its own.
func ratioCI(pairs []pair, pick func(pair) (float64, float64)) (ratio, lo, hi float64, ok bool) {
	// A bootstrap resamples tasks, so with one or two of them every
	// resample is the same handful of values and the interval collapses to
	// a point — which then reads as certainty. That is precisely the
	// overclaim this file exists to prevent, so below minPairs the ratio
	// is reported without an interval and never called significant.
	const minPairs = 3
	if len(pairs) < minPairs {
		if len(pairs) == 0 {
			return 0, 0, 0, false
		}
	}
	sum := func(idx []int) (b, c float64) {
		for _, i := range idx {
			pb, pc := pick(pairs[i])
			b, c = b+pb, c+pc
		}
		return
	}
	all := make([]int, len(pairs))
	for i := range all {
		all[i] = i
	}
	b, cu := sum(all)
	if b <= 0 {
		return 0, 0, 0, false
	}
	ratio = cu / b
	if len(pairs) < minPairs {
		return ratio, 0, 0, false
	}
	rng := rand.New(rand.NewSource(1))
	const resamples = 4000
	ratios := make([]float64, 0, resamples)
	idx := make([]int, len(pairs))
	for range resamples {
		for i := range idx {
			idx[i] = rng.Intn(len(pairs))
		}
		rb, rc := sum(idx)
		if rb > 0 {
			ratios = append(ratios, rc/rb)
		}
	}
	if len(ratios) < resamples/2 {
		return ratio, 0, 0, false
	}
	sort.Float64s(ratios)
	return ratio, ratios[int(0.025*float64(len(ratios)))], ratios[int(0.975*float64(len(ratios)))-1], true
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
	c.pairCosts(base, cur)
	return c
}

// pairCosts bootstraps the cost and context ratios over the tasks the two
// sets share.
func (c *Comparison) pairCosts(base, cur []Result) {
	bc, bctx, bout, bknown := perTask(base)
	cc, cctx, cout, cknown := perTask(cur)
	var pairs []pair
	for name, b := range bc {
		cv, ok := cc[name]
		if !ok {
			continue
		}
		pairs = append(pairs, pair{base: b, cur: cv, baseCtx: bctx[name], curCtx: cctx[name], baseOut: bout[name], curOut: cout[name]})
	}
	if len(pairs) == 0 {
		return
	}
	c.Paired = true
	if bknown && cknown {
		c.CostRatio, c.CostRatioLo, c.CostRatioHi, _ = ratioCI(pairs, func(p pair) (float64, float64) { return p.base, p.cur })
	}
	c.ContextRatio, c.ContextRatioLo, c.ContextRatioHi, _ = ratioCI(pairs, func(p pair) (float64, float64) { return p.baseCtx, p.curCtx })
	c.OutputRatio, c.OutputRatioLo, c.OutputRatioHi, _ = ratioCI(pairs, func(p pair) (float64, float64) { return p.baseOut, p.curOut })
}

// perTask is each task's mean cost and mean context tokens across its runs.
func perTask(rs []Result) (cost, ctx, out map[string]float64, costKnown bool) {
	cost, ctx, out = map[string]float64{}, map[string]float64{}, map[string]float64{}
	n := map[string]int{}
	costKnown = true
	for _, r := range rs {
		cost[r.Name] += r.CostUSD
		ctx[r.Name] += float64(contextTokens(r.Usage))
		out[r.Name] += float64(r.Usage.OutputTokens)
		n[r.Name]++
		costKnown = costKnown && r.CostKnown
	}
	for name, k := range n {
		cost[name] /= float64(k)
		ctx[name] /= float64(k)
		out[name] /= float64(k)
	}
	return cost, ctx, out, costKnown
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
		fmt.Fprintf(w, "cost per solved task %s → %s (%s%s)%s\n", llm.FormatUSD(c.Base.CostPerSolved), llm.FormatUSD(c.Current.CostPerSolved), sign, llm.FormatUSD(math.Abs(c.CostDelta)),
			ratioNote(c.CostRatio, c.CostRatioLo, c.CostRatioHi, c.Paired && c.CostRatio > 0))
	}
	if c.Base.Passed > 0 && c.Current.Passed > 0 {
		ratio := float64(c.Current.ContextPerSolved) / math.Max(float64(c.Base.ContextPerSolved), 1)
		fmt.Fprintf(w, "context per solved task %s → %s (×%.2f) · compactions %d → %d%s\n",
			shortTokens(c.Base.ContextPerSolved), shortTokens(c.Current.ContextPerSolved), ratio, c.Base.Compactions, c.Current.Compactions,
			ratioNote(c.ContextRatio, c.ContextRatioLo, c.ContextRatioHi, c.Paired))
	}
	if c.Paired && c.OutputRatio > 0 {
		fmt.Fprintf(w, "output tokens %s → %s · generation is most of a turn's latency%s\n",
			shortTokens(c.Base.Usage.OutputTokens), shortTokens(c.Current.Usage.OutputTokens),
			ratioNote(c.OutputRatio, c.OutputRatioLo, c.OutputRatioHi, true))
	}
	if c.Paired && !c.CostSignificant() && !c.ContextSignificant() {
		fmt.Fprintf(w, "neither cost nor context moved beyond the run-to-run spread: this is not evidence of a saving.\n")
	}
	if c.Base.FactsAsked > 0 && c.Current.FactsAsked > 0 {
		fmt.Fprintf(w, "facts recalled after compaction %d/%d → %d/%d\n", c.Base.FactsCorrect, c.Base.FactsAsked, c.Current.FactsCorrect, c.Current.FactsAsked)
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

// ratioNote is the paired-bootstrap interval for a ratio, and the plain
// statement of what it means. A ratio without its interval is the thing
// that produced a withdrawn "12% better" on this task set, so the interval
// is printed on the same line as the number, not somewhere below it.
func ratioNote(r, lo, hi float64, ok bool) string {
	if !ok || r == 0 {
		return ""
	}
	if lo == 0 && hi == 0 {
		return fmt.Sprintf("\n  paired ×%.2f over shared tasks · too few tasks to bootstrap an interval", r)
	}
	verdict := "inside the noise"
	switch {
	case hi < 1:
		verdict = "real saving"
	case lo > 1:
		verdict = "real increase"
	}
	return fmt.Sprintf("\n  paired ×%.2f [%.2f, %.2f] over shared tasks · %s", r, lo, hi, verdict)
}

func summaryLine(s Summary) string {
	cost := "cost unknown"
	if s.CostKnown {
		cost = "cost " + llm.FormatUSD(s.CostUSD)
		if s.Passed > 0 {
			cost += " (" + llm.FormatUSD(s.CostPerSolved) + " per solved)"
		}
	}
	timing := ""
	if s.ModelTime > 0 {
		pct := func(d time.Duration) float64 { return 100 * d.Seconds() / math.Max(s.Duration.Seconds(), 1) }
		timing = fmt.Sprintf(" · %.0f%% waiting on the model, %.0f%% in tools", pct(s.ModelTime), pct(s.ToolTime))
	}
	fresh := ""
	if c := s.Context; c > 0 {
		f := freshTokens(s.Usage)
		fresh = fmt.Sprintf(" · %s of it fresh (%.0f%%)", shortTokens(f), 100*float64(f)/float64(c))
	}
	ctx := fmt.Sprintf("context %s (in %s · cache %s · out %s)", shortTokens(s.Context), shortTokens(s.Usage.InputTokens), shortTokens(s.Usage.CacheReadTokens), shortTokens(s.Usage.OutputTokens))
	if s.Passed > 0 {
		ctx += fmt.Sprintf(" · %s per solved", shortTokens(s.ContextPerSolved))
	}
	ctx += fresh
	if s.Compactions > 0 {
		ctx += fmt.Sprintf(" · %d compaction(s), summaries %s in / %s out, %d expand(s)", s.Compactions, shortTokens(contextTokens(s.SummaryUsage)), shortTokens(s.SummaryUsage.OutputTokens), s.Expands)
	}
	if saves := s.Recoveries + s.WatchdogKills + s.ModelFallbacks; saves > 0 {
		// What the harness had to rescue for these runs to get this far.
		// Without it a pass rate reads as the model's, and a harness
		// change that repairs more is indistinguishable from one that
		// needs to repair less.
		ctx += fmt.Sprintf("\nharness repairs: %d (%d tool call(s) read back out of prose · %d stream(s) ended by the watchdog · %d call(s) that only worked on a fallback model)",
			saves, s.Recoveries, s.WatchdogKills, s.ModelFallbacks)
	}
	if s.FactsAsked > 0 {
		ctx += fmt.Sprintf("\nfacts after compaction: %d/%d recalled (%.0f%%) · %d still in the archive · %d actually retrieved · %d grounded · %d unknown · %d searched",
			s.FactsCorrect, s.FactsAsked, 100*float64(s.FactsCorrect)/float64(s.FactsAsked), s.FactsInArchive, s.FactsRetrieved, s.FactsGrounded, s.FactsUnknown, s.FactsFetched)
	}
	return fmt.Sprintf("%d/%d passed (%.0f%%, 95%% CI %.0f–%.0f%%) · %d task(s) × %d run(s) · %d tool calls · %s%s · %s\n%s",
		s.Passed, s.Total, 100*s.PassRate, 100*s.Lo, 100*s.Hi, s.Tasks, s.Runs, s.ToolCalls, s.Duration.Round(time.Second), timing, cost, ctx)
}
