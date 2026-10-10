// Package localize turns a red test run after a multi-hunk edit into
// causal facts about the edit. In a throwaway worktree it puts the files
// the turn changed back the way the turn found them, re-applies the
// turn's hunks in subsets and reruns only the failing tests, narrowing
// with delta debugging (ddmin) to the fewest hunks that still make them
// fail: "hunk 3 of 7 did it". It then runs the failing package's tests
// one at a time with coverage and ranks the functions the failing ones
// executed by Ochiai suspiciousness.
//
// Both are computed by the harness and attached to the failing output;
// the model never has to ask. Everything is bounded by a time budget and
// the text by a byte budget, and a failure that predates the turn, does
// not reproduce in a clean copy, or comes from a runner this package does
// not know produces nothing.
//
// One runner is implemented, `go test` (see Go); Lang is the seam for
// pytest with coverage.py and node with c8, which need only name their
// failures, rerun a subset and report per-test coverage. The throwaway
// worktree comes from internal/localize/tree, which also has the Run the
// composition root hands the agent.
package localize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/diff"
)

// Test is one top-level test of one package (a Go import path, a pytest
// file, a node test file).
type Test struct {
	Pkg, Name string
}

func (t Test) String() string { return t.Name }

// Outcome is one rerun of some tests.
type Outcome struct {
	Failed map[Test]bool
	// Judged is false when the run says nothing about the tests: a
	// package did not build, or the run was cut off.
	Judged bool
	Output string
}

// Lang is one test runner.
type Lang interface {
	Name() string
	// Failures names the failing tests in a red run's output; none when
	// the output is not this runner's.
	Failures(output string) []Test
	// Run reruns exactly these tests in dir.
	Run(ctx context.Context, dir string, tests []Test) Outcome
	// Spectra runs tests of the failing tests' packages one at a time
	// with coverage, at most max of them, the failing ones first, and
	// returns what it has when ctx ends.
	Spectra(ctx context.Context, dir string, failing []Test, max int) ([]Spectrum, error)
}

// Langs are the runners tried, in order, when a Request names none.
var Langs = []Lang{Go{}}

// Edit is one file as the turn found it and as it is now. Path is
// relative to the project root.
type Edit struct {
	Path    string
	Existed bool // the file was there when the turn found it
	Before  []byte
	Exists  bool // the file is there now
	After   []byte
}

// Unit is one hunk of one Edit: what ddmin adds and takes away.
type Unit struct {
	Edit int // index into Request.Edits
	Hunk change.Hunk
}

// Request is one red run to localize.
type Request struct {
	Root   string // the project directory
	Edits  []Edit
	Output string // the red run's output
	// Budget bounds the whole localization; zero means DefaultBudget.
	Budget time.Duration
	// MaxBytes bounds Report.Text; zero means DefaultMaxBytes.
	MaxBytes int
	// MaxTests bounds the tests run with coverage; zero means DefaultMaxTests.
	MaxTests int
	// Langs are the runners to try; nil means Langs.
	Langs []Lang
	// Scratch makes the throwaway copy of the repository as it stands
	// that every rerun happens in, and says where Root is inside it.
	// internal/localize/tree.Scratch is the one janetcode uses; it lives
	// apart so the agent loop, which needs only this package's types,
	// does not import the worktree machinery.
	Scratch func(ctx context.Context, root string) (dir string, cleanup func(), err error)
	// Log receives progress lines when set.
	Log func(string)
}

const (
	// DefaultBudget bounds one localization: every rerun is a build and
	// a test run, and the model is waiting on the result.
	DefaultBudget = 60 * time.Second
	// DefaultMaxBytes bounds the text attached to the failing output.
	DefaultMaxBytes = 2048
	// DefaultMaxTests bounds the coverage runs behind the ranking.
	DefaultMaxTests = 24
	// MinHunks is the fewest hunks worth minimising over: with one,
	// the answer is known before anything runs.
	MinHunks = 2
	// maxUnits bounds the hunks considered; past it a turn rewrote too
	// much for subsets to finish inside any sensible budget.
	maxUnits = 64
	// maxFileBytes skips a file too large to diff by lines cheaply.
	maxFileBytes = 1 << 20
	// maxSuspects is how many ranked functions the text lists.
	maxSuspects = 5
)

// Report is what localization found.
type Report struct {
	Lang  string
	Tests []Test // the failures the turn caused, which were localized
	Units []Unit
	Edits []Edit
	// Minimal indexes Units: the hunks that alone still make Tests fail.
	Minimal []int
	// Complete says ddmin ran to the end, so Minimal is 1-minimal.
	Complete bool
	// OthersPass says the tests pass with every hunk but Minimal, when
	// OthersChecked; with them, removing Minimal alone fixes the run.
	OthersChecked, OthersPass bool
	Runs                      int
	Suspects                  []Suspect
	Spectra                   int // tests run with coverage
	Duration                  time.Duration
	// Skipped says why nothing was localized.
	Skipped string
}

// Units splits the edits into hunks, in file order.
func Units(edits []Edit) []Unit {
	var out []Unit
	for i, e := range edits {
		for _, h := range hunks(e) {
			out = append(out, Unit{Edit: i, Hunk: h})
		}
	}
	return out
}

func hunks(e Edit) []change.Hunk {
	if len(e.Before) > maxFileBytes || len(e.After) > maxFileBytes {
		return nil
	}
	return change.MakeHunks(change.DiffLines(diff.SplitLinesKeepEOL(string(e.Before)), diff.SplitLinesKeepEOL(string(e.After))))
}

// Materialize is the edit's file with only the kept hunks applied to what
// the turn found; exists is false when that leaves no file (it was
// created by an unkept hunk, or deleted by a kept one that took all of it).
func Materialize(e Edit, hs []change.Hunk, keep func(i int) bool) (content []byte, exists bool) {
	before := diff.SplitLinesKeepEOL(string(e.Before))
	var out []string
	pos, any, all := 0, false, true
	for i, h := range hs {
		// OldStart is where the hunk's lines begin; for a pure insertion
		// with no context before it, the line it goes in front of.
		start := min(max(h.OldStart-1, pos), len(before))
		out = append(out, before[pos:start]...)
		old, new := h.Sides()
		if keep(i) {
			out = append(out, new...)
			any = true
		} else {
			out = append(out, old...)
			all = false
		}
		pos = min(start+h.OldCount, len(before))
	}
	out = append(out, before[pos:]...)
	switch {
	case !e.Existed && !any:
		return nil, false
	case !e.Exists && all:
		return nil, false
	}
	return []byte(strings.Join(out, "")), true
}

// Run localizes the failure in req.Output. It never fails the caller: a
// localization that cannot run says why in Report.Skipped.
func Run(ctx context.Context, req Request) (rep Report) {
	start := time.Now()
	rep.Edits = req.Edits
	defer func() { rep.Duration = time.Since(start) }()
	rep.Units = Units(req.Edits)
	if len(rep.Units) < MinHunks {
		rep.Skipped = "fewer than two hunks"
		return rep
	}
	if len(rep.Units) > maxUnits {
		rep.Skipped = fmt.Sprintf("%d hunks, more than %d", len(rep.Units), maxUnits)
		return rep
	}
	langs := req.Langs
	if langs == nil {
		langs = Langs
	}
	var lang Lang
	var failing []Test
	for _, l := range langs {
		if f := l.Failures(req.Output); len(f) > 0 {
			lang, failing = l, f
			break
		}
	}
	if lang == nil {
		rep.Skipped = "no failing tests a known runner names"
		return rep
	}
	rep.Lang = lang.Name()
	budget := req.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if req.Scratch == nil {
		rep.Skipped = "no throwaway worktree"
		return rep
	}
	dir, cleanup, err := req.Scratch(ctx, req.Root)
	if err != nil {
		rep.Skipped = "no throwaway worktree: " + err.Error()
		return rep
	}
	defer cleanup()
	perEdit := make([][]change.Hunk, len(req.Edits))
	first := make([]int, len(req.Edits)) // each edit's first unit
	for i := range rep.Units {
		u := rep.Units[i]
		if perEdit[u.Edit] == nil {
			first[u.Edit] = i
		}
		perEdit[u.Edit] = append(perEdit[u.Edit], u.Hunk)
	}
	apply := func(subset []int) error {
		in := map[int]bool{}
		for _, i := range subset {
			in[i] = true
		}
		for ei, e := range req.Edits {
			if len(perEdit[ei]) == 0 {
				continue
			}
			content, exists := Materialize(e, perEdit[ei], func(h int) bool { return in[first[ei]+h] })
			p := filepath.Join(dir, filepath.FromSlash(e.Path))
			if !exists {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, content, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	run := func(c context.Context, subset []int, tests []Test) (Outcome, error) {
		if err := apply(subset); err != nil {
			return Outcome{}, err
		}
		rep.Runs++
		return lang.Run(c, dir, tests), nil
	}
	logf := func(format string, a ...any) {
		if req.Log != nil {
			req.Log(fmt.Sprintf(format, a...))
		}
	}
	all := make([]int, len(rep.Units))
	for i := range all {
		all[i] = i
	}

	// What the turn found: a test failing there too is not this turn's
	// doing, and minimising over hunks cannot explain it.
	none, err := run(ctx, nil, failing)
	if err != nil {
		rep.Skipped = err.Error()
		return rep
	}
	if !none.Judged {
		rep.Skipped = "the tests do not build or finish without this turn's edits"
		return rep
	}
	var caused []Test
	for _, t := range failing {
		if !none.Failed[t] {
			caused = append(caused, t)
		}
	}
	if len(caused) == 0 {
		rep.Skipped = "the failing tests fail without this turn's edits too"
		return rep
	}
	full, err := run(ctx, all, caused)
	if err != nil {
		rep.Skipped = err.Error()
		return rep
	}
	rep.Tests = nil
	for _, t := range caused {
		if full.Failed[t] {
			rep.Tests = append(rep.Tests, t)
		}
	}
	if !full.Judged || len(rep.Tests) == 0 {
		rep.Skipped = "the failure did not reproduce in a clean copy (flaky, or it depends on something outside the tree)"
		return rep
	}
	logf("localize: %d hunks, %d failing test(s) caused by the turn", len(rep.Units), len(rep.Tests))

	// ddmin gets two thirds of what is left; coverage the rest, and
	// whatever ddmin did not need.
	dl, _ := ctx.Deadline()
	dctx, dcancel := context.WithDeadline(ctx, time.Now().Add(time.Until(dl)*2/3))
	fails := func(subset []int) (bool, error) {
		if dctx.Err() != nil {
			return false, errOutOfBudget
		}
		o, err := run(dctx, subset, rep.Tests)
		if err != nil {
			return false, err
		}
		if !o.Judged {
			if dctx.Err() != nil {
				return false, errOutOfBudget
			}
			return false, nil
		}
		for _, t := range rep.Tests {
			if o.Failed[t] {
				return true, nil
			}
		}
		return false, nil
	}
	minimal, complete, err := ddmin(all, fails)
	if err != nil {
		dcancel()
		rep.Skipped = err.Error()
		return rep
	}
	rep.Minimal, rep.Complete = minimal, complete
	if len(minimal) < len(all) && dctx.Err() == nil {
		var others []int
		in := map[int]bool{}
		for _, i := range minimal {
			in[i] = true
		}
		for _, i := range all {
			if !in[i] {
				others = append(others, i)
			}
		}
		if o, err := run(dctx, others, rep.Tests); err == nil && o.Judged {
			rep.OthersChecked = true
			rep.OthersPass = true
			for _, t := range rep.Tests {
				if o.Failed[t] {
					rep.OthersPass = false
				}
			}
		}
	}
	dcancel()
	logf("localize: %d of %d hunks are enough (%d runs)", len(minimal), len(all), rep.Runs)

	if ctx.Err() == nil {
		if err := apply(all); err == nil {
			max := req.MaxTests
			if max <= 0 {
				max = DefaultMaxTests
			}
			spectra, _ := lang.Spectra(ctx, dir, rep.Tests, max)
			rep.Spectra = len(spectra)
			rep.Suspects = ochiai(spectra)
			markEdited(rep.Suspects, req.Edits, rep.Units)
		}
	}
	return rep
}

// markEdited flags each suspect one of the turn's hunks touches, judged
// by the line numbers the hunk occupies in the file as it is now.
func markEdited(sus []Suspect, edits []Edit, units []Unit) {
	for i := range sus {
		for _, u := range units {
			if filepath.ToSlash(edits[u.Edit].Path) != sus[i].Path {
				continue
			}
			lo, hi := u.Hunk.NewStart, u.Hunk.NewStart+u.Hunk.NewCount-1
			if lo <= sus[i].End && hi >= sus[i].Line {
				sus[i].Edited = true
			}
		}
	}
}

// Found reports whether there is anything to attach.
func (r Report) Found() bool { return r.Skipped == "" && len(r.Tests) > 0 }

// Summary is one line for notices.
func (r Report) Summary() string {
	if r.Skipped != "" {
		return "localize skipped: " + r.Skipped
	}
	return fmt.Sprintf("localize: %d of %d hunks make %s fail · %d runs · %s", len(r.Minimal), len(r.Units), testList(r.Tests, 3), r.Runs, r.Duration.Round(100*time.Millisecond))
}

// Text is what the model reads, at most maxBytes long (zero means
// DefaultMaxBytes). It states only what the runs showed.
func (r Report) Text(maxBytes int) string {
	if !r.Found() {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	var b strings.Builder
	n := len(r.Units)
	tests := testList(r.Tests, 3)
	switch {
	case len(r.Minimal) == n:
		fmt.Fprintf(&b, "[localize] %s fails only with all %d of this turn's hunks applied together; no smaller subset of them fails it (%d reruns in a clean copy).\n", tests, n, r.Runs)
	default:
		qual := "is enough"
		if len(r.Minimal) > 1 {
			qual = "are enough"
		}
		fmt.Fprintf(&b, "[localize] Of the %d hunks this turn changed, %d %s to make %s fail on the code as the turn found it", n, len(r.Minimal), qual, tests)
		if r.Complete && len(r.Minimal) > 1 {
			b.WriteString(", and with any one of them left out it passes")
		} else if !r.Complete {
			b.WriteString(" (time ran out before a smaller set was ruled out)")
		}
		switch {
		case r.OthersChecked && r.OthersPass:
			fmt.Fprintf(&b, "; with the other %d hunks alone it passes", n-len(r.Minimal))
		case r.OthersChecked:
			fmt.Fprintf(&b, "; the other %d hunks alone fail it too", n-len(r.Minimal))
		}
		fmt.Fprintf(&b, " (%d reruns in a clean copy).\n", r.Runs)
	}
	head := b.String()
	var suspects strings.Builder
	if len(r.Suspects) > 0 {
		fmt.Fprintf(&suspects, "Most suspicious functions (Ochiai, from %d tests run alone with coverage):\n", r.Spectra)
		for i, s := range r.Suspects {
			if i == maxSuspects {
				break
			}
			fmt.Fprintf(&suspects, "- %.2f %s:%d %s (failing tests ran it %d, passing %d)", s.Score, s.Path, s.Line, s.Name, s.Failed, s.Passed)
			if s.Edited {
				suspects.WriteString(", edited this turn")
			}
			suspects.WriteString("\n")
		}
	}
	room := maxBytes - len(head) - suspects.Len()
	var hunks strings.Builder
	if len(r.Minimal) < n {
		for k, i := range sortedCopy(r.Minimal) {
			s := r.hunkText(i)
			if hunks.Len()+len(s) > room {
				fmt.Fprintf(&hunks, "- … and %d more\n", len(r.Minimal)-k)
				break
			}
			hunks.WriteString(s)
		}
	}
	out := head + hunks.String() + suspects.String()
	if len(out) > maxBytes {
		out = cutLines(out, maxBytes)
	}
	return strings.TrimRight(out, "\n")
}

// hunkText is one hunk for the model: where it is and its changed lines.
func (r Report) hunkText(i int) string {
	u := r.Units[i]
	path := r.Edits[u.Edit].Path
	var b strings.Builder
	lo, hi := changedLines(u.Hunk)
	where := fmt.Sprintf("line %d", lo)
	if hi > lo {
		where = fmt.Sprintf("lines %d-%d", lo, hi)
	}
	fmt.Fprintf(&b, "- hunk %d of %d: %s %s\n", i+1, len(r.Units), path, where)
	shown := 0
	for _, l := range u.Hunk.Lines {
		if l[0] == ' ' {
			continue
		}
		if shown == 8 {
			b.WriteString("    …\n")
			break
		}
		line := strings.TrimRight(l, "\r\n")
		if len(line) > 160 {
			line = line[:160] + "…"
		}
		b.WriteString("    " + line + "\n")
		shown++
	}
	return b.String()
}

// changedLines is the span of a hunk's changed lines, context left out,
// numbered as in the file now; a pure deletion is the line it left
// behind.
func changedLines(h change.Hunk) (lo, hi int) {
	n := h.NewStart
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			n++
			continue
		case '+':
			n++
			if lo == 0 {
				lo = n - 1
			}
			hi = n - 1
			continue
		}
		if lo == 0 {
			lo = n
		}
		hi = max(hi, n)
	}
	return lo, hi
}

func testList(ts []Test, n int) string {
	var names []string
	for i, t := range ts {
		if i == n {
			names = append(names, fmt.Sprintf("%d more", len(ts)-n))
			break
		}
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

func sortedCopy(s []int) []int {
	c := append([]int(nil), s...)
	sort.Ints(c)
	return c
}

// cutLines keeps whole lines up to max bytes.
func cutLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		s = s[:i+1]
	}
	return s
}
