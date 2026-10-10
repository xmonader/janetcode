package editpattern

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The offline pre-screen (the idea's "Test:" line): of the recorded runs,
// how many made three or more structurally similar hunks? The idea is
// dropped under 10%.
//
// It replays each run's edits in order. Two edits that Induce a rule open
// a class; a later edit joins the class only when the rule, applied to
// that edit's old text, gives its new text — so a counted third hunk is
// one the harness could have written itself, not merely one of a similar
// shape. That is the stricter reading of "structurally similar", and the
// one that measures what the mechanism would actually have saved.

// Run is one recorded session's edits, in the order the model made them.
type Run struct {
	Name  string
	Edits []Edit
}

// Class is one induced rule within a run.
type Class struct {
	Rule Rule
	// Hunks is how many of the run's edits the rule covers, the two it
	// was induced from included.
	Hunks int
	// Bytes is the new-text bytes of the hunks after the second: what the
	// model wrote out that an offer would have spared it.
	Bytes int
}

// RunResult is one run's screen.
type RunResult struct {
	Name    string
	Edits   int
	Classes []Class
}

// Best is the run's largest class (zero value when it has none).
func (r RunResult) Best() Class {
	var best Class
	for _, c := range r.Classes {
		if c.Hunks > best.Hunks {
			best = c
		}
	}
	return best
}

// ScreenRun replays one run's edits.
func ScreenRun(run Run) RunResult {
	res := RunResult{Name: run.Name}
	seen := map[Edit]bool{}
	var pending []Edit
	for _, e := range run.Edits {
		// A retried identical edit (the first failed to match) is one hunk.
		if seen[e] || sameTokens(e.Old, e.New) {
			continue
		}
		seen[e] = true
		res.Edits++
		if res.join(e) {
			continue
		}
		induced := false
		for i := len(pending) - 1; i >= 0; i-- {
			if r, ok := Induce(pending[i], e); ok {
				res.Classes = append(res.Classes, Class{Rule: r, Hunks: 2})
				pending = append(pending[:i], pending[i+1:]...)
				induced = true
				break
			}
		}
		if !induced {
			pending = append(pending, e)
		}
	}
	return res
}

func (res *RunResult) join(e Edit) bool {
	for i := range res.Classes {
		if res.Classes[i].Rule.Predicts(e) {
			res.Classes[i].Hunks++
			res.Classes[i].Bytes += len(e.New)
			return true
		}
	}
	return false
}

// Result is the screen over many runs.
type Result struct {
	Runs []RunResult
	// EditBytes is the new-text bytes of every counted edit, the
	// denominator for the bytes a rule would have saved.
	EditBytes int
}

// Screen runs ScreenRun over every run.
func Screen(runs []Run) Result {
	var out Result
	for _, r := range runs {
		out.Runs = append(out.Runs, ScreenRun(r))
		seen := map[Edit]bool{}
		for _, e := range r.Edits {
			if !seen[e] {
				seen[e] = true
				out.EditBytes += len(e.New)
			}
		}
	}
	return out
}

// Counts returns how many runs edited at all, how many induced a rule
// (two similar hunks) and how many had three or more.
func (s Result) Counts() (editing, pairs, threes int) {
	for _, r := range s.Runs {
		if r.Edits > 0 {
			editing++
		}
		switch b := r.Best(); {
		case b.Hunks >= 3:
			threes++
			pairs++
		case b.Hunks == 2:
			pairs++
		}
	}
	return
}

// Text renders the screen and its verdict against the idea's 10% line.
func (s Result) Text() string {
	editing, pairs, threes := s.Counts()
	saved, sites := 0, 0
	var hits []RunResult
	for _, r := range s.Runs {
		for _, c := range r.Classes {
			saved += c.Bytes
			if c.Hunks > 2 {
				sites += c.Hunks - 2
			}
		}
		if r.Best().Hunks >= 3 {
			hits = append(hits, r)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "runs %d, with edits %d\n", len(s.Runs), editing)
	fmt.Fprintf(&b, "two similar hunks (a rule induced): %d (%s of editing runs)\n", pairs, pct(pairs, editing))
	fmt.Fprintf(&b, "three or more (the screen):         %d (%s of editing runs, %s of all)\n",
		threes, pct(threes, editing), pct(threes, len(s.Runs)))
	fmt.Fprintf(&b, "hunks a rule would have written:    %d, %d of %d edit bytes (%s)\n",
		sites, saved, s.EditBytes, pct(saved, s.EditBytes))
	verdict := "keep: at or above the 10% line"
	if editing == 0 || threes*10 < editing {
		verdict = "drop: under the 10% line"
	}
	fmt.Fprintf(&b, "verdict: %s\n", verdict)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Best().Hunks > hits[j].Best().Hunks })
	for _, r := range hits {
		c := r.Best()
		fmt.Fprintf(&b, "  %-60s %d hunks  %s\n", r.Name, c.Hunks, clip(c.Rule.String(), 90))
	}
	return b.String()
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// EditsFromEvents extracts the edit_file hunks from a janetcode session,
// in order. A multi-edit call contributes each of its edits; line-range
// and anchor edits carry no old text and are left out.
func EditsFromEvents(events []session.Event) []Edit {
	var out []Edit
	for _, ev := range events {
		if ev.Type != "assistant/message" || ev.Message == nil {
			continue
		}
		for _, b := range ev.Message.Blocks {
			if b.Type != llm.BlockToolUse || b.Name != "edit_file" {
				continue
			}
			out = append(out, editFileInput(b.Input)...)
		}
	}
	return out
}

func editFileInput(raw json.RawMessage) []Edit {
	type one struct {
		Old string `json:"old_string"`
		New string `json:"new_string"`
	}
	var in struct {
		Path string `json:"path"`
		one
		Edits []one `json:"edits"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []Edit
	for _, e := range append([]one{in.one}, in.Edits...) {
		if e.Old != "" {
			out = append(out, Edit{Path: in.Path, Old: e.Old, New: e.New})
		}
	}
	return out
}
