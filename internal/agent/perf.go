package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// This file answers one question: where did the time go? /stats counts
// what happened, and a session that felt slow needs the other half — how
// the wall clock was divided between the provider, the tools, the person
// at the keyboard and the harness's own loop. Every figure here is
// measured, never modelled: a slice nobody instrumented shows up in
// "harness" rather than being quietly attributed.

// waitMeter accumulates the time one tool call spent blocked on the
// user. A permission prompt happens inside the call, so without this the
// call that waited four minutes for an answer reads as a four-minute
// tool. Tool calls can run concurrently, so the meter travels on the
// call's own context rather than sitting on the agent.
type waitMeter struct {
	mu sync.Mutex
	d  time.Duration
}

func (w *waitMeter) add(d time.Duration) {
	w.mu.Lock()
	w.d += d
	w.mu.Unlock()
}

func (w *waitMeter) total() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d
}

type waitMeterKey struct{}

// meterWaits returns a context whose blocking user prompts are counted
// into the returned meter.
func meterWaits(ctx context.Context) (context.Context, *waitMeter) {
	w := &waitMeter{}
	return context.WithValue(ctx, waitMeterKey{}, w), w
}

// waited records that the harness blocked on the user for d: it is time
// the session spent, but it is not time the model or a tool took, and
// the two must not be confused.
func (a *Agent) waited(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	a.stats.waited(d)
	if w, ok := ctx.Value(waitMeterKey{}).(*waitMeter); ok {
		w.add(d)
	}
}

// PerfSlice is one row of the wall-clock budget: how long, how many
// things made it up, and the longest single one of them. Slowest is what
// turns "tools took two minutes" into "one grep took ninety seconds".
type PerfSlice struct {
	Name    string
	Time    time.Duration
	Count   int
	Slowest time.Duration
	Note    string
}

// Perf divides the time spent inside turns. The slices are disjoint and
// sum to TurnTime by construction: harness is the remainder, so anything
// not instrumented is visible rather than lost.
//
// Tool time already has the user's thinking taken out of it (see
// waitMeter), so "you" is a slice of its own and not a tax on bash.
func (s Stats) Perf() []PerfSlice {
	var toolTime, toolSlowest time.Duration
	toolCalls := 0
	for _, t := range s.Tools {
		toolTime += t.Time
		toolCalls += t.Calls
		if t.Slowest > toolSlowest {
			toolSlowest = t.Slowest
		}
	}
	var modelSlowest time.Duration
	for _, m := range s.Models {
		if m.Slowest > modelSlowest {
			modelSlowest = m.Slowest
		}
	}
	modelNote := "waiting for the provider to answer"
	if s.ModelTTFT > 0 && s.ModelTime > 0 {
		// The half of the diagnosis a total cannot give you: a provider
		// that takes a minute to start and one that takes a minute to
		// finish are different problems with the same number.
		modelNote = fmt.Sprintf("%s of it (%d%%) before the first token, the rest generating",
			textfmt.Duration(s.ModelTTFT), pct(s.ModelTTFT, s.ModelTime))
	}
	out := []PerfSlice{
		{Name: "model", Time: s.ModelTime, Count: s.ModelCalls, Slowest: modelSlowest, Note: modelNote},
		{Name: "tools", Time: toolTime, Count: toolCalls, Slowest: toolSlowest, Note: "running what the model asked for"},
		{Name: "you", Time: s.WaitTime, Count: s.WaitCount, Note: "prompts and questions waiting on an answer"},
	}
	rest := s.TurnTime - s.ModelTime - toolTime - s.WaitTime
	if rest < 0 {
		// Concurrent tool calls overlap, so their times can add up to
		// more than the turn's wall clock. Saying so beats a negative.
		out = append(out, PerfSlice{Name: "harness", Note: "overlapped: tool calls ran in parallel"})
		return out
	}
	out = append(out, PerfSlice{Name: "harness", Time: rest, Note: "the loop itself: context, hooks, checkpoints, logging"})
	return out
}

// FormatPerf is the /perf report, worded once so both front-ends say the
// same thing. open is how long the session has been up, which is what
// the in-turn total is a fraction of: the rest of it is reading and
// typing, and a report that hid that would make the harness look slower
// than it is.
func FormatPerf(s Stats, open time.Duration) string {
	var sb strings.Builder
	if s.TurnTime <= 0 {
		return "nothing has run yet: no turn has been timed, so there is no time to account for"
	}
	fmt.Fprintf(&sb, "where the time went · %s open · %s in %s",
		textfmt.Duration(open), textfmt.Duration(s.TurnTime), textfmt.Plural(s.Turns, "turn"))
	if open > 0 && s.TurnTime < open {
		fmt.Fprintf(&sb, " (%d%%; the rest is you reading and typing)", pct(s.TurnTime, open))
	}
	for _, p := range s.Perf() {
		fmt.Fprintf(&sb, "\n  %-8s %9s", p.Name, textfmt.Duration(p.Time))
		if s.TurnTime > 0 {
			fmt.Fprintf(&sb, " %3d%%", pct(p.Time, s.TurnTime))
		} else {
			sb.WriteString("     ")
		}
		if p.Count > 0 {
			fmt.Fprintf(&sb, " · %d", p.Count)
			if p.Time > 0 {
				fmt.Fprintf(&sb, " · avg %s", textfmt.Duration(p.Time/time.Duration(p.Count)))
			}
			if p.Slowest > 0 {
				fmt.Fprintf(&sb, " · slowest %s", textfmt.Duration(p.Slowest))
			}
		}
		if p.Note != "" {
			fmt.Fprintf(&sb, " · %s", p.Note)
		}
	}
	if len(s.Models) > 0 {
		sb.WriteString("\nby model")
		for _, m := range sortedByTime(s.Models) {
			fmt.Fprintf(&sb, "\n  %-24s %9s · %s", m.Name, textfmt.Duration(m.Time), textfmt.Plural(m.Calls, "call"))
			if m.Calls > 0 && m.Time > 0 {
				fmt.Fprintf(&sb, " · avg %s", textfmt.Duration(m.Time/time.Duration(m.Calls)))
			}
			if m.Slowest > 0 {
				fmt.Fprintf(&sb, " · slowest %s", textfmt.Duration(m.Slowest))
			}
			if m.TTFT > 0 {
				fmt.Fprintf(&sb, " · %s waiting to start", textfmt.Duration(m.TTFT))
			}
			if m.Errors > 0 {
				fmt.Fprintf(&sb, " · %d failed", m.Errors)
			}
		}
	}
	if slow := slowestTools(s.Tools); len(slow) > 0 {
		sb.WriteString("\nby tool, slowest first")
		for _, t := range slow {
			fmt.Fprintf(&sb, "\n  %-16s %9s · %s", t.Name, textfmt.Duration(t.Time), textfmt.Plural(t.Calls, "call"))
			if t.Calls > 0 && t.Time > 0 {
				fmt.Fprintf(&sb, " · avg %s", textfmt.Duration(t.Time/time.Duration(t.Calls)))
			}
			if t.Slowest > 0 {
				fmt.Fprintf(&sb, " · slowest %s", textfmt.Duration(t.Slowest))
			}
			if t.Errors > 0 {
				fmt.Fprintf(&sb, " · %d failed", t.Errors)
			}
		}
	}
	return sb.String()
}

// pct is a percentage that never divides by zero and never reads as 0%
// for something that did happen.
func pct(part, whole time.Duration) int {
	if whole <= 0 {
		return 0
	}
	p := int(part * 100 / whole)
	if p == 0 && part > 0 {
		return 1
	}
	return p
}

func sortedByTime(ms []ModelStat) []ModelStat {
	out := append([]ModelStat(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// slowestTools is the tools worth looking at, longest total first. Tools
// that took no measurable time are left out: a report is read, and a
// dozen rows of 0s is not.
func slowestTools(ts []ToolStat) []ToolStat {
	out := make([]ToolStat, 0, len(ts))
	for _, t := range ts {
		if t.Time > 0 {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}
