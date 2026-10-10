// Package difffuzz turns "this change keeps behaviour the same" from a
// claim into a fact, or into a counterexample. When a turn refactors,
// optimises or cleans up code, the changed functions are compiled twice —
// as they were before the turn and as they are now — and fed the same
// generated inputs for a few seconds. The first input on which the two
// disagree is the report: "Parse("a,,b"): before ["a" "" "b"], after
// ["a" "b"]". A public test suite checks the inputs someone thought of;
// this checks the ones nobody did, against the only oracle a refactor
// has, the old code.
//
// Each language is a Backend. Go is the one there is: it builds a
// native fuzz harness (testing.F) calling each changed exported function
// in the package as it is and in a copy of the package as it was, put
// under an alias import path in a throwaway copy of the module (see
// gobackend.go). Another language plugs in by implementing Backend and
// adding itself to Backends; nothing else here knows about Go.
//
// Nothing here writes to the workspace: every build and every run happens
// in a temporary copy, which is removed afterwards.
package difffuzz

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// File is one file the change touched: as it was before and as it is now.
type File struct {
	Rel             string // slash-separated, relative to Request.Root
	Before, After   []byte
	Existed, Exists bool
}

// Request is one comparison.
type Request struct {
	Root  string // the workspace the files are relative to
	Files []File
	// Budget is the fuzzing time across every function compared; the
	// build and the seed run come on top of it.
	Budget time.Duration
	// MaxTargets bounds how many functions are fuzzed; zero is 8.
	MaxTargets int
	// Env is added to the environment of every command a backend runs.
	Env []string
}

// Divergence is the first input on which the old and new code disagree.
type Divergence struct {
	Func  string `json:"func"`
	Input string `json:"input"` // the call as Go source: Parse("a,,b")
	Old   string `json:"old"`
	New   string `json:"new"`
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: before %s, after %s", d.Input, d.Old, d.New)
}

// Report is what a comparison found.
type Report struct {
	Backend string `json:"backend,omitempty"`
	// Targets are the functions compared; Passed is true for each that
	// ran its whole budget without a disagreement.
	Targets []Target `json:"targets,omitempty"`
	// Diverged is the first disagreement, nil when there was none.
	Diverged *Divergence `json:"diverged,omitempty"`
	// Skipped says why nothing was compared: no backend applies, no
	// changed function the backend can call, the copy could not build.
	Skipped string `json:"skipped,omitempty"`
	// Inconclusive is a run that ended without a verdict on a function:
	// a hang, an out-of-memory, a crash outside the compared calls.
	Inconclusive []string `json:"inconclusive,omitempty"`
	// Output is the tail of what the failing command printed, kept for a
	// build error or an inconclusive run.
	Output  string        `json:"output,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns,omitempty"`
}

// Target is one function compared.
type Target struct {
	Package string `json:"package"`
	Func    string `json:"func"`
	Passed  bool   `json:"passed"`
	// Why is why it was chosen: its body changed, or it calls something
	// that did.
	Why string `json:"why,omitempty"`
}

// Summary is the report in one line.
func (r Report) Summary() string {
	switch {
	case r.Skipped != "":
		return "differential fuzz skipped: " + r.Skipped
	case r.Diverged != nil:
		return fmt.Sprintf("differential fuzz: %s behaves differently from the code before the change — %s", r.Diverged.Func, r.Diverged)
	}
	var names []string
	for _, t := range r.Targets {
		names = append(names, t.Func)
	}
	s := fmt.Sprintf("differential fuzz: %d changed function(s) agree with the code before the change (%s)", len(r.Targets), strings.Join(names, ", "))
	if len(r.Inconclusive) > 0 {
		s += "; inconclusive: " + strings.Join(r.Inconclusive, ", ")
	}
	return s
}

// Backend compares old and new code for one language.
type Backend interface {
	// Name is the language, for the report.
	Name() string
	// Applies reports whether any of the files is this backend's to
	// compare.
	Applies(files []File) bool
	// Run does the comparison. It must not write under req.Root.
	Run(ctx context.Context, req Request) Report
}

// Backends are tried in order; the first that applies runs.
var Backends = []Backend{goBackend{}}

// Run compares the change with the first backend that applies.
func Run(ctx context.Context, req Request) Report {
	if req.MaxTargets <= 0 {
		req.MaxTargets = 8
	}
	if req.Budget <= 0 {
		req.Budget = 20 * time.Second
	}
	for _, b := range Backends {
		if b.Applies(req.Files) {
			start := time.Now()
			r := b.Run(ctx, req)
			r.Backend, r.Elapsed = b.Name(), time.Since(start)
			return r
		}
	}
	return Report{Skipped: "no changed file in a language the fuzzer knows (Go)"}
}

var (
	// keepRe is a task that says outright that behaviour must not change.
	keepRe = regexp.MustCompile(`(?i)(without chang\w* (?:\w+ ){0,2}behaviou?r|behaviou?r (?:must|should|has to|needs to) (?:not change|stay|remain|be (?:kept|preserved|unchanged|the same))|(?:no|zero) (?:functional|behaviou?ral|observable|behaviou?r) change|preserv\w* (?:\w+ ){0,2}behaviou?r|keep\w* (?:the )?(?:same |existing |current )?behaviou?r|(?:must|should) behave (?:exactly )?the same|behaviou?r[- ]preserving|same (?:outputs?|results?) (?:as before|for every input))`)
	// refactorRe is a task whose verb implies the behaviour stays: a
	// refactor, a clean-up, an optimisation.
	refactorRe = regexp.MustCompile(`(?i)\b(refactor\w*|restructur\w*|clean(?:s|ed|ing)? ?up|tidy|tidi\w*|simplif\w*|optimi[sz]\w*|speed up|make \w+ faster|deduplicat\w*|de-?dup\w*|extract (?:a |the )?(?:helper|function|method)|inline|rewrite \w+ (?:for|to be) (?:speed|clarity|readability))\b`)
	// changeRe is a task that also asks for new behaviour, where a
	// difference from the old code is the point.
	changeRe = regexp.MustCompile(`(?i)\b(fix\w*|bug\w*|add\w*|implement\w*|support\w*|feature\w*|new (?:option|flag|command|endpoint)|handle\w*|reject\w*|validat\w*|change (?:the )?(?:output|format|behaviou?r))\b`)
)

// BehaviourPreserving reports whether a task says its change keeps
// behaviour the same: outright ("without changing behaviour"), or by its
// verb (refactor, optimise, clean up) when it asks for nothing new beside
// it. A task that also fixes or adds something is out: there the old code
// is not an oracle.
func BehaviourPreserving(task string) bool {
	if keepRe.MatchString(task) {
		return true
	}
	return refactorRe.MatchString(task) && !changeRe.MatchString(task)
}
