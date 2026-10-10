package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/speccov"
)

const speccovBrief = "# Task\n\n" +
	"* `parse` must reject `1 +` with `unexpected end`.\n" +
	"* division by zero raises `division by zero at 7`.\n" +
	"* names are looked up in `env_vars`.\n\n" +
	"```python\nfrom calc import evaluate\nevaluate(\"1+2\") -> 3\nevaluate(\"2*3\") -> 6\n```\n"

// speccovAgent is an agent rooted in a workspace holding the brief and a
// public test that reaches only the first rule.
func speccovAgent(t *testing.T, f *fakeProbes) *Agent {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "BRIEF.md"), []byte(speccovBrief), 0o644)
	os.WriteFile(filepath.Join(root, "test_calc.py"), []byte("from calc import evaluate\n\ndef test_a():\n    assert 'unexpected end'\n"), 0o644)
	a := &Agent{Log: &session.Memory{}, Root: root}
	if f != nil {
		a.probeExec = f.exec
	}
	a.integ.userText = "Read BRIEF.md and implement the full task."
	a.turnN = 1
	return a
}

// The matrix note names the rules no public test reaches, once per task
// text, and only when switched on.
func TestSpecCovStepNote(t *testing.T) {
	a := speccovAgent(t, nil)
	if note := a.specCovStepNote(func(Event) {}); note != "" {
		t.Fatalf("a note while off: %q", note)
	}
	a.SetSpecCoverage(true)
	var notices []string
	note := a.specCovStepNote(func(e Event) { notices = append(notices, e.Text) })
	if !strings.Contains(note, "division by zero at 7") || !strings.Contains(note, "env_vars") || strings.Contains(note, "unexpected end") {
		t.Fatalf("note = %q", note)
	}
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "spec coverage: ") {
		t.Fatalf("notices = %q", notices)
	}
	// Later steps of the turn, and later turns on the same text, say
	// nothing more.
	if a.specCovStepNote(func(Event) {}) != "" {
		t.Fatal("noted twice in a turn")
	}
	a.turnN = 2
	if a.specCovStepNote(func(Event) {}) != "" {
		t.Fatal("noted twice for one task text")
	}
	// JANETCODE_OFF wins over the config.
	a.turnN, a.specCov.notedText = 3, ""
	a.Off = map[string]bool{"speccov": true}
	if a.specCovStepNote(func(Event) {}) != "" {
		t.Fatal("noted with speccov off")
	}
}

// A short request is not a spec.
func TestSpecCovStepNoteNeedsASpec(t *testing.T) {
	a := speccovAgent(t, nil)
	a.SetSpecCoverage(true)
	a.integ.userText = "fix the typo"
	if note := a.specCovStepNote(func(Event) {}); note != "" {
		t.Fatalf("note for a one-line request: %q", note)
	}
}

// speccovExampleCmds are the compiled commands of the brief's two
// examples, in order.
func speccovExampleCmds(t *testing.T, a *Agent) []string {
	t.Helper()
	text, _ := speccov.TaskText(a.Root, a.integ.userText)
	sp := speccov.Extract(text)
	var out []string
	for _, p := range speccov.Harvest(sp, speccov.Imports(sp, speccov.PublicTests(a.Root))) {
		out = append(out, p.Cmd)
	}
	if len(out) != 2 {
		t.Fatalf("harvested %d examples, want 2", len(out))
	}
	return out
}

func (a *Agent) testSpecExampleGate() bool {
	return a.specExampleGate(context.Background(), func(Event) {})
}

// A failing example sends the turn back with the example named, at most
// maxProbeRounds times; nothing runs before a change or twice on one tree.
func TestSpecExampleGateSendsBackAFailingExample(t *testing.T) {
	f := &fakeProbes{now: map[string]probeOutcome{}}
	a := speccovAgent(t, f)
	cmds := speccovExampleCmds(t, a)
	f.now[cmds[0]], f.now[cmds[1]] = probePass, probeFail
	a.SetSpecExamples(true)
	if a.testSpecExampleGate() || len(f.runs) != 0 {
		t.Fatal("the gate ran before any change")
	}
	a.mutations = 1
	for round := 1; round <= maxProbeRounds; round++ {
		if !a.testSpecExampleGate() {
			t.Fatalf("round %d: a failing example was not sent back", round)
		}
		if got := lastHarnessText(a); !strings.Contains(got, `evaluate("2*3") -> 6`) || strings.Contains(got, `"1+2"`) {
			t.Fatalf("feedback = %q", got)
		}
		n := len(f.runs)
		if a.testSpecExampleGate() || len(f.runs) != n {
			t.Fatal("the gate reran on an unchanged tree")
		}
		a.mutations++
	}
	if a.testSpecExampleGate() {
		t.Fatal("sent back past the round limit")
	}
	// In the next turn the model has declined it: dropped, not sent back.
	a.turnN, a.mutations = 2, 1
	if a.testSpecExampleGate() {
		t.Fatal("a declined example was sent back in a later turn")
	}
	if !a.specCov.pending[1].done {
		t.Fatal("the declined example was kept")
	}
}

// A passing example joins the probe ledger when that is on, and is then
// the ledger's to replay; an example that cannot run here is never run
// again and never counts as a failure.
func TestSpecExampleGateHandsOffAndRetires(t *testing.T) {
	f := &fakeProbes{now: map[string]probeOutcome{}}
	a := speccovAgent(t, f)
	cmds := speccovExampleCmds(t, a)
	f.now[cmds[0]] = probePass // cmds[1] is missing: inconclusive
	a.SetSpecExamples(true)
	a.ProbeLedger = true
	a.mutations = 1
	if a.testSpecExampleGate() {
		t.Fatal("sent back with nothing failing")
	}
	if len(a.probes.frozen) != 1 || a.probes.frozen[0].cmd != cmds[0] {
		t.Fatalf("ledger = %+v", a.probes.frozen)
	}
	a.mutations++
	n := len(f.runs)
	a.testSpecExampleGate()
	if len(f.runs) != n {
		t.Fatalf("ran %q again", f.runs[n:])
	}
}

// The not-runnable marker is inconclusive even though the run exited
// non-zero.
func TestSpecExampleNotRunnableIsNotAFailure(t *testing.T) {
	a := speccovAgent(t, nil)
	a.probeExec = func(context.Context, string) probeResult {
		return probeResult{outcome: probeFail, output: speccov.NotRunnable + " NameError('x')"}
	}
	a.SetSpecExamples(true)
	a.mutations = 1
	if a.testSpecExampleGate() {
		t.Fatal("an example that could not run sent the turn back")
	}
}
