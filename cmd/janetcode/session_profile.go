package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/session"
)

// `--session-profile <paths>` answers the question every context-curation
// idea has to answer before it is built: where do the calls and the input
// tokens actually go? It reads recorded runs — an eval result file
// (--eval-json), a session .jsonl, or a directory of either — and prints
// one row per model, counted from the log alone with no decisions call.
//
// The three numbers it exists for are ceilings, not measurements of any
// feature: the share of calls spent exploring bounds what a context
// pre-selector can save; the calls before the first edit bound how much of
// that a turn-start injection can reach; and the share of input tokens that
// is tool output older than N calls and never referenced again bounds what
// pruning at the compaction boundary could ever recover.

// profileStaleAfter is the default age, in model calls, past which a tool
// result nobody touched again counts as dead weight. Six is the tail
// compaction keeps verbatim (agent's tailMessages), so a result older than
// that is one compaction would be free to drop.
const profileStaleAfter = 6

func sessionProfileMode(w io.Writer, path string, staleAfter int) error {
	files, err := profileFiles(path)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("session-profile: no session or eval result files under %s", path)
	}
	byModel := map[string]*agent.CallProfile{}
	var read, skipped int
	for _, f := range files {
		runs, err := profileRuns(f, staleAfter)
		if err != nil {
			skipped++
			fmt.Fprintf(w, "skipped %s: %v\n", filepath.Base(f), err)
			continue
		}
		read++
		for _, p := range runs {
			agg := byModel[p.Model]
			if agg == nil {
				agg = &agent.CallProfile{Model: p.Model, ToolPhase: map[agent.Phase]int{}, ModelPhase: map[agent.Phase]int{}, Waste: map[string]int{}}
				byModel[p.Model] = agg
			}
			agg.Add(p)
		}
	}
	if len(byModel) == 0 {
		return fmt.Errorf("session-profile: %d files read, no runs with recorded events", read)
	}
	writeProfileTable(w, byModel, staleAfter, read, skipped)
	return nil
}

// profileFiles is one file, or every .json and .jsonl under a directory.
func profileFiles(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && (strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".jsonl")) {
			out = append(out, filepath.Join(path, n))
		}
	}
	sort.Strings(out)
	return out, nil
}

// profileRuns reads one file either way round: an eval result file holds
// many runs under a shared model, a session .jsonl holds one.
func profileRuns(path string, staleAfter int) ([]*agent.CallProfile, error) {
	if strings.HasSuffix(path, ".jsonl") {
		dir, id := filepath.Split(path)
		sess, _, err := session.Open(filepath.Clean(dir), strings.TrimSuffix(id, ".jsonl"))
		if err != nil {
			return nil, err
		}
		model := sess.Meta.Model
		if model == "" {
			model = "unknown"
		}
		// A recorded session has no pass/fail bit, so the dead-tail rule
		// cannot apply; the other two waste labels still do.
		return []*agent.CallProfile{agent.ProfileRun(model, sess.Events(), staleAfter, true)}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f eval.File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	model := f.Model
	if model == "" {
		model = "unknown"
	}
	var out []*agent.CallProfile
	for i := range f.Results {
		if len(f.Results[i].Events) == 0 {
			continue
		}
		out = append(out, agent.ProfileRun(model, f.Results[i].Events, staleAfter, f.Results[i].Passed))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no events recorded (run with --eval-json on a build that logs events)")
	}
	return out, nil
}

func writeProfileTable(w io.Writer, byModel map[string]*agent.CallProfile, staleAfter, read, skipped int) {
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)

	fmt.Fprintf(w, "session profile: %d files read", read)
	if skipped > 0 {
		fmt.Fprintf(w, ", %d skipped", skipped)
	}
	fmt.Fprintf(w, ", stale after %d model calls\n\n", staleAfter)

	for _, m := range models {
		p := byModel[m]
		fmt.Fprintf(w, "%s (%d runs)\n", m, p.Runs)
		fmt.Fprintf(w, "  model calls %d, tool calls %d, compactions %d\n", p.ModelCalls, p.ToolCalls, p.Compactions)
		fmt.Fprintf(w, "  %-8s %10s %10s\n", "phase", "tool", "model")
		for _, ph := range agent.Phases {
			fmt.Fprintf(w, "  %-8s %6d %3.0f%% %6d %3.0f%%\n", ph,
				p.ToolPhase[ph], profilePct(p.ToolPhase[ph], p.ToolCalls),
				p.ModelPhase[ph], profilePct(p.ModelPhase[ph], p.ModelCalls))
		}
		fmt.Fprintf(w, "  calls before first edit: %s (over %d of %d runs that edited)\n",
			mean(p.CallsBeforeFirstEdit, p.EditingRuns), p.EditingRuns, p.Runs)
		fmt.Fprintf(w, "  input per call: first %s, last %s, growth %s tokens/call\n",
			mean(p.InputFirst, p.Runs), mean(p.InputLast, p.Runs), meanF(p.InputGrowth, p.GrowthRuns))
		fmt.Fprintf(w, "  input tokens (fresh+cache read): %d\n", p.InputTokens)
		fmt.Fprintf(w, "  of that, tool output carried forward: %d (%.0f%%)\n",
			p.ToolOutputPaid, profilePct(p.ToolOutputPaid, p.InputTokens))
		fmt.Fprintf(w, "  of that, stale (>%d calls old, never referenced again): %d (%.1f%% of input, %.0f%% of tool output)\n",
			staleAfter, p.ToolOutputStale, profilePct(p.ToolOutputStale, p.InputTokens), profilePct(p.ToolOutputStale, p.ToolOutputPaid))
		fmt.Fprintf(w, "  results never referenced again: %d of %d (%.0f%%)\n",
			p.StaleResults, p.Results, profilePct(p.StaleResults, p.Results))
		wasted := p.Waste[agent.WasteErrored] + p.Waste[agent.WasteLoop] + p.Waste[agent.WasteDeadTail]
		fmt.Fprintf(w, "  wasted tool calls: %d of %d (%.1f%%) -- errored %d, loop %d, dead tail %d\n",
			wasted, p.ToolCalls, profilePct(wasted, p.ToolCalls),
			p.Waste[agent.WasteErrored], p.Waste[agent.WasteLoop], p.Waste[agent.WasteDeadTail])
		writeProfileSpread(w, p.Points)
		fmt.Fprintln(w)
	}
}

func profilePct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func mean(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", float64(n)/float64(d))
}

func meanF(n float64, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f", n/float64(d))
}

// writeProfileSpread turns the per-run rows into the step-level metrics an
// A/B is read on, each with a bootstrap interval over runs. The unit of
// resampling is the run, because that is the unit that was randomised: a
// run's own steps are not independent of each other.
func writeProfileSpread(w io.Writer, points []agent.RunPoint) {
	if len(points) < 2 {
		return
	}
	rows := []struct {
		label string
		of    func(agent.RunPoint) float64
	}{
		{"explore model calls", func(r agent.RunPoint) float64 { return float64(r.Explore) }},
		{"model calls before first edit", func(r agent.RunPoint) float64 { return float64(r.BeforeFirstEdit) }},
		{"wasted tool calls", func(r agent.RunPoint) float64 { return float64(r.Wasted) }},
		{"model calls", func(r agent.RunPoint) float64 { return float64(r.ModelCalls) }},
		{"tool calls", func(r agent.RunPoint) float64 { return float64(r.ToolCalls) }},
		{"input tokens (k)", func(r agent.RunPoint) float64 { return float64(r.InputTokens) / 1000 }},
	}
	fmt.Fprintf(w, "  per run (mean [95%% bootstrap], n=%d):\n", len(points))
	for _, row := range rows {
		vals := make([]float64, len(points))
		for i, pt := range points {
			vals[i] = row.of(pt)
		}
		m, lo, hi := meanCI(vals, 2000, 7)
		fmt.Fprintf(w, "    %-30s %7.1f  [%.1f, %.1f]\n", row.label, m, lo, hi)
	}
	var passed int
	for _, pt := range points {
		if pt.Passed {
			passed++
		}
	}
	fmt.Fprintf(w, "    %-30s %7d / %d\n", "passed", passed, len(points))
}

// meanCI is a percentile bootstrap of the mean. The seed is fixed so two
// readings of the same file print the same interval.
func meanCI(vals []float64, resamples int, seed int64) (mean, lo, hi float64) {
	n := len(vals)
	if n == 0 {
		return 0, 0, 0
	}
	for _, v := range vals {
		mean += v
	}
	mean /= float64(n)
	if n < 2 {
		return mean, mean, mean
	}
	rnd := rand.New(rand.NewSource(seed))
	means := make([]float64, resamples)
	for i := range means {
		var s float64
		for j := 0; j < n; j++ {
			s += vals[rnd.Intn(n)]
		}
		means[i] = s / float64(n)
	}
	sort.Float64s(means)
	return mean, means[resamples*25/1000], means[resamples*975/1000]
}
