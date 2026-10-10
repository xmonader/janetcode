package rollout

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

// The offline pre-screens: both mechanisms are judged on recorded runs
// first, at zero tokens, before any live experiment is paid for. Each
// asks one question the report's kill criteria turn on.
//
//   - The branch retry's: of the runs that ended badly, how many have a
//     branch point at all, and how much of each run a branch would replay
//     instead of buying again. Whether the branch then recovers the run
//     needs a live run; this only says whether there is anything to branch.
//   - The second sample's: of the runs that finished green, how many the
//     trigger fires on, and what fraction of those actually failed the
//     hidden tests — its precision, which the report kills below 40%.

// Run is one recorded headless run with its grade.
type Run struct {
	// Name identifies it in the report (campaign/task-seed).
	Name   string
	Events []session.Event
	// Err is the result object's error text, "" for a clean exit.
	Err string
	// Pass is the hidden-test grade.
	Pass bool
}

// BranchRow is one bad-ended run's branch point.
type BranchRow struct {
	Name     string
	End      string
	Pass     bool
	Critical Critical
}

// Screen is both pre-screens over one set of runs.
type Screen struct {
	Runs int

	// Bad is every run with a bad end (BadEndText), the retry's trigger.
	Bad []BranchRow
	// Other is the runs that failed the hidden tests or errored without a
	// bad end — a dropped connection, a wall-clock kill, a clean exit with
	// wrong code: the retry does not fire on them, and they are listed so
	// the size of what it cannot reach is visible.
	Other []BranchRow

	// Green is the runs that ended clean; GreenFail those of them that
	// failed the hidden tests (the base rate the trigger must beat).
	Green, GreenFail int
	// Triggered is the green runs Weak fires on, TriggeredFail those of
	// them that failed the hidden tests; Reasons counts each reason's
	// first word.
	Triggered, TriggeredFail int
	Reasons                  map[string]int
	// ReasonsFail counts, per reason, the triggered runs that failed:
	// each reason's own precision, so a noisy one can be told apart.
	ReasonsFail   map[string]int
	TriggeredRuns []string
}

// RunScreen folds runs into both pre-screens.
func RunScreen(runs []Run) Screen {
	s := Screen{Runs: len(runs), Reasons: map[string]int{}, ReasonsFail: map[string]int{}}
	for _, r := range runs {
		end := BadEndText(r.Err, r.Events)
		if end != EndClean {
			s.Bad = append(s.Bad, BranchRow{Name: r.Name, End: string(end), Pass: r.Pass, Critical: CriticalStep(r.Events)})
		} else if !r.Pass || r.Err != "" {
			why := "wrong answer"
			if r.Err != "" {
				why = "error: " + firstLine(r.Err, 50)
			}
			s.Other = append(s.Other, BranchRow{Name: r.Name, End: why, Pass: r.Pass, Critical: CriticalStep(r.Events)})
		}
		if r.Err != "" || GateRed(r.Events) {
			continue
		}
		s.Green++
		if !r.Pass {
			s.GreenFail++
		}
		why := Weak(r.Events)
		if len(why) == 0 {
			continue
		}
		s.Triggered++
		if !r.Pass {
			s.TriggeredFail++
		}
		for _, w := range why {
			k := strings.SplitN(w, ":", 2)[0]
			s.Reasons[k]++
			if !r.Pass {
				s.ReasonsFail[k]++
			}
		}
		s.TriggeredRuns = append(s.TriggeredRuns, fmt.Sprintf("%s (%s) %s", r.Name, grade(r.Pass), strings.Join(why, "; ")))
	}
	return s
}

// Precision is the second sample's trigger precision; ok is false when it
// never fired, which is no number at all rather than zero.
func (s Screen) Precision() (p float64, ok bool) {
	if s.Triggered == 0 {
		return 0, false
	}
	return float64(s.TriggeredFail) / float64(s.Triggered), true
}

// Text renders the screen for a terminal.
func (s Screen) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d recorded runs\n\n", s.Runs)
	b.WriteString("checkpoint-branch retry (--branch-retry)\n")
	branchable := 0
	var shares []float64
	for _, r := range s.Bad {
		if r.Critical.OK() {
			branchable++
			shares = append(shares, r.Critical.Share())
		}
	}
	fmt.Fprintf(&b, "  runs with a bad end (the trigger): %d, %d of them with a branch point\n", len(s.Bad), branchable)
	if len(shares) > 0 {
		sort.Float64s(shares)
		fmt.Fprintf(&b, "  model calls a branch replays instead of buying: median %.0f%% (min %.0f%%, max %.0f%%)\n",
			100*shares[len(shares)/2], 100*shares[0], 100*shares[len(shares)-1])
	}
	writeRows(&b, s.Bad)
	otherBranchable := 0
	for _, r := range s.Other {
		if r.Critical.OK() {
			otherBranchable++
		}
	}
	fmt.Fprintf(&b, "  failed or errored runs it does not fire on: %d, %d of them with a branch point\n", len(s.Other), otherBranchable)
	writeRows(&b, s.Other)
	// Not the rule, reported beside it: where the branch would start if
	// it kept every call before the first edit rather than only the
	// exploring ones (a run whose prompt inlined the repository spends
	// its pre-edit calls planning, not reading).
	pre := 0
	var preShares []float64
	for _, r := range append(append([]BranchRow{}, s.Bad...), s.Other...) {
		if c := r.Critical; c.FirstEdit > 1 && c.Calls > 0 {
			pre++
			preShares = append(preShares, float64(c.FirstEdit-1)/float64(c.Calls))
		}
	}
	if pre > 0 {
		sort.Float64s(preShares)
		fmt.Fprintf(&b, "  (not the rule) runs above with model calls before their first edit: %d, median %.0f%% of their calls\n", pre, 100*preShares[len(preShares)/2])
	}

	b.WriteString("\nverdict-gated second sample (--second-sample)\n")
	fmt.Fprintf(&b, "  green runs: %d, %d of them failed the hidden tests (base rate %s)\n", s.Green, s.GreenFail, pct(s.GreenFail, s.Green))
	fmt.Fprintf(&b, "  trigger fired: %d", s.Triggered)
	if len(s.Reasons) > 0 {
		var rs []string
		for k, v := range s.Reasons {
			rs = append(rs, fmt.Sprintf("%s %d, %d failing", k, v, s.ReasonsFail[k]))
		}
		sort.Strings(rs)
		fmt.Fprintf(&b, " (%s)", strings.Join(rs, ", "))
	}
	b.WriteString("\n")
	if p, ok := s.Precision(); ok {
		fmt.Fprintf(&b, "  precision: %d/%d = %.0f%% of triggered runs failed the hidden tests (kill below 40%%)\n", s.TriggeredFail, s.Triggered, 100*p)
	} else {
		b.WriteString("  precision: undefined, the trigger never fired\n")
	}
	fmt.Fprintf(&b, "  recall: %d of the %d green-but-failing runs triggered\n", s.TriggeredFail, s.GreenFail)
	for _, r := range s.TriggeredRuns {
		fmt.Fprintf(&b, "    %s\n", r)
	}
	return b.String()
}

func writeRows(b *strings.Builder, rows []BranchRow) {
	for _, r := range rows {
		c := r.Critical
		point := "no branch point"
		if c.OK() {
			point = fmt.Sprintf("branch after call %d of %d (%.0f%% replayed, %d things explored)", c.Step, c.Calls, 100*c.Share(), c.Explored)
		} else if c.Step > 0 {
			point = fmt.Sprintf("explored through its last call (%d of %d)", c.Step, c.Calls)
		}
		edit := "never edited"
		if c.FirstEdit > 0 {
			edit = fmt.Sprintf("first edit at call %d", c.FirstEdit)
		}
		fmt.Fprintf(b, "    %s (%s, %s): %s, %s\n", r.Name, r.End, grade(r.Pass), point, edit)
	}
}

func grade(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func pct(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}

func firstLine(s string, n int) string {
	s = strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
