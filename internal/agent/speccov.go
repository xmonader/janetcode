package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/speccov"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The spec-coverage matrix and example harvesting (internal/speccov). The
// misses left on effbench are hidden-test rules no public test reaches;
// both mechanisms read the task text — the prompt and the documents it
// names, BRIEF.md on effbench — with no model call of their own:
//
//   - SpecCoverage: at the turn's first tool step the rules and examples
//     of the task text are matched against the public tests, and the
//     model is told which ones no public test names anything of. Opt-in
//     (config spec_coverage): it adds text to what the model reads.
//   - SpecExamples: the worked examples Harvest can compile (a Python call
//     with a literal answer, a shell command with its output) run at each
//     gate after a change. One that fails goes back to the model like a
//     failed check; one that passes is handed to the probe ledger when
//     that is on, which replays it at every later gate. Opt-in (config
//     spec_examples): it runs commands taken from the task text and can
//     send a turn back. Each run goes through the bash tool and the
//     permission rules, as the ledger's do.
//
// JANETCODE_OFF=speccov and JANETCODE_OFF=specexamples turn them off
// again.

// Limits: at most specMaxExamples examples are kept, and a turn is sent
// back for failing ones at most maxProbeRounds times, the ledger's bound.
// A task text with fewer than specMinItems items is a request, not a spec.
const (
	specMaxExamples = 12
	specMinItems    = 3
)

// specCovState is the session's spec-coverage state.
type specCovState struct {
	// coverage and examples are the switches, under featMu.
	coverage, examples bool
	// notedTurn is the last turn the matrix was considered in, notedText
	// the task text it was last given for: the same text is not noted
	// twice in a session.
	notedTurn int
	notedText string
	// harvested is the task text the examples were last read from.
	harvested string
	pending   []specExample
	// gateTurn and gateSeen are the turn and a.mutations when the
	// examples last ran; rounds counts this turn's send-backs.
	gateTurn, gateSeen, rounds int
}

// specExample is one compiled example and what the gate made of it.
type specExample struct {
	speccov.Probe
	// done: handed to the probe ledger, found not runnable here, or
	// declined by the model; it is not run again.
	done bool
	// disputedTurn is the turn it was last sent back in, 0 if never.
	disputedTurn int
}

// SetSpecCoverage turns the spec-coverage note on or off.
func (a *Agent) SetSpecCoverage(on bool) {
	a.featMu.Lock()
	a.specCov.coverage = on
	a.featMu.Unlock()
}

// SpecCoverageOn reports whether the spec-coverage note is on.
func (a *Agent) SpecCoverageOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.specCov.coverage && !a.Off["speccov"]
}

// SetSpecExamples turns example harvesting on or off.
func (a *Agent) SetSpecExamples(on bool) {
	a.featMu.Lock()
	a.specCov.examples = on
	a.featMu.Unlock()
}

// SpecExamplesOn reports whether example harvesting is on.
func (a *Agent) SpecExamplesOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.specCov.examples && !a.Off["specexamples"]
}

// specCovStepNote is the matrix note for this step's results: given once,
// at the turn's first tool step, for a task text not noted before in the
// session. Usually that step is the model reading the brief, before any
// edit.
func (a *Agent) specCovStepNote(emit func(Event)) string {
	st := &a.specCov
	if !a.SpecCoverageOn() || st.notedTurn == a.turnN || a.readOnly() {
		return ""
	}
	st.notedTurn = a.turnN
	root := a.displayRoot()
	text, _ := speccov.TaskText(root, a.integ.userText)
	if text == st.notedText {
		return ""
	}
	st.notedText = text
	sp := speccov.Extract(text)
	if len(sp.Items) < specMinItems {
		return ""
	}
	m := speccov.Cover(sp, speccov.PublicTests(root))
	summary := fmt.Sprintf("%d rules and examples in the task text: %d uncovered, %d covered, %d unanchored · %s",
		len(m.Rows), m.Count(speccov.Uncovered), m.Count(speccov.Covered), m.Count(speccov.Unanchored), textfmt.Plural(len(m.Tests), "public test file"))
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "speccov/matrix", Output: summary})
	note := speccov.Note(m)
	if note != "" {
		emit(Event{Kind: EvNotice, Text: "spec coverage: " + summary})
	}
	return note
}

// specHarvest compiles the examples of the current task text, adding the
// ones not already held.
func (a *Agent) specHarvest() {
	st := &a.specCov
	root := a.displayRoot()
	text, _ := speccov.TaskText(root, a.integ.userText)
	if text == st.harvested {
		return
	}
	st.harvested = text
	sp := speccov.Extract(text)
	have := map[string]bool{}
	for _, e := range st.pending {
		have[e.Cmd] = true
	}
	for _, p := range speccov.Harvest(sp, speccov.Imports(sp, speccov.PublicTests(root))) {
		if len(st.pending) >= specMaxExamples {
			break
		}
		if !have[p.Cmd] {
			have[p.Cmd] = true
			st.pending = append(st.pending, specExample{Probe: p})
		}
	}
}

// specExampleGate runs the task text's examples when the turn has changed
// something since they last ran. It reports whether the turn was sent
// back because one fails.
func (a *Agent) specExampleGate(ctx context.Context, emit func(Event)) bool {
	st := &a.specCov
	if !a.SpecExamplesOn() || a.mutations == 0 || st.gateTurn == a.turnN && st.gateSeen == a.mutations || a.readOnly() || a.DryRunOn() {
		return false
	}
	if st.gateTurn != a.turnN {
		st.rounds = 0
	}
	st.gateTurn, st.gateSeen = a.turnN, a.mutations
	a.specHarvest()
	ledger := a.ProbeLedgerOn()
	var failing []failedProbe
	passed, handed, unrunnable := 0, 0, 0
	for i := range st.pending {
		ex := &st.pending[i]
		if ex.done {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		res := a.runProbe(ctx, ex.Cmd, "")
		switch {
		case res.outcome == probeInconclusive || strings.Contains(res.output, speccov.NotRunnable):
			// Says nothing about the code, and will not on the next
			// tree either: the example is beyond what this workspace
			// can run.
			ex.done = true
			unrunnable++
		case res.outcome == probePass:
			passed++
			if ledger {
				a.probes.freeze(Probe{Cmd: ex.Cmd, Key: ex.Cmd})
				ex.done = true
				handed++
			}
		case ex.disputedTurn != 0 && ex.disputedTurn != a.turnN:
			emit(Event{Kind: EvNotice, Text: "spec examples: dropping an example the model left failing last turn: " + ex.Label})
			ex.done = true
		default:
			failing = append(failing, failedProbe{ex.Label, res.output})
			ex.disputedTurn = a.turnN
		}
	}
	if passed+len(failing)+unrunnable > 0 {
		summary := fmt.Sprintf("%d passing (%d handed to the probe ledger), %d failing, %d not runnable here",
			passed, handed, len(failing), unrunnable)
		a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "speccov/examples", Output: summary})
		emit(Event{Kind: EvNotice, Text: "spec examples: " + summary})
	}
	if len(failing) == 0 {
		return false
	}
	if st.rounds >= maxProbeRounds {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("spec examples: %s still failing after %d rounds; not sending the model back again", textfmt.Plural(len(failing), "example"), maxProbeRounds)})
		return false
	}
	st.rounds++
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("spec examples: %s from the task text fail on the code; sending the model back (%d/%d)", textfmt.Plural(len(failing), "worked example"), st.rounds, maxProbeRounds)})
	a.appendHarnessText(specExampleFeedback(failing))
	return true
}

// specExampleFeedback is what the model reads when worked examples of the
// task text fail.
func specExampleFeedback(failing []failedProbe) string {
	var b strings.Builder
	b.WriteString("[harness] The task text gives worked examples with their answers. The harness ran these against your code and the answers differ:\n")
	per := 4000 / max(1, len(failing))
	for _, f := range failing {
		fmt.Fprintf(&b, "\n%s\n```\n%s\n```\n", f.key, textfmt.TruncateTo(strings.TrimSpace(f.output), per))
	}
	b.WriteString("\nMake the code give the stated answers, then run the project's tests once. If the harness misread an example, say so in one line and finish.")
	return b.String()
}
