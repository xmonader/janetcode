package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestIsProbe(t *testing.T) {
	for cmd, want := range map[string]bool{
		"python3 - <<'EOF'\nfrom m import f\nassert f(2) == 4\nEOF":      true,
		`python3 -c "import m; assert m.f(1) == 2"`:                      true,
		`node -e "if (f(1) !== 2) throw new Error('bad')"`:               true,
		`cd /w && python -c "import sys, m; sys.exit(m.f() != 3)"`:       true,
		`python3 -c "import m; print(m.f(1))"`:                           false, // cannot fail on purpose
		`pytest -q && python3 -c "assert True"`:                          false, // the project's tests, with a tail
		`go test ./... -run TestAssert`:                                  false,
		`grep -rn assert src/`:                                           false,
		"python3 - <<'EOF'\n# tests for the rules\nassert g() == 1\nEOF": true, // "tests" in the script body is not a test run
	} {
		if got := IsProbe(cmd); got != want {
			t.Errorf("IsProbe(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// probeEvents builds a turn's tool results; each entry is a bash command
// with its output, or "edit" for a successful write.
func probeEvents(turn int, calls ...[2]string) []session.Event {
	var ev []session.Event
	for i, c := range calls {
		e := session.Event{Type: session.TypeToolResult, Turn: turn, Seq: i + 1, CallID: "c" + string(rune('a'+i))}
		if c[0] == "edit" {
			e.Name, e.Arguments, e.Output = "write_file", `{"path":"m.py","content":"x"}`, "Wrote m.py"
		} else {
			args, _ := json.Marshal(map[string]string{"command": c[0]})
			e.Name, e.Arguments, e.Output = "bash", string(args), c[1]
		}
		ev = append(ev, e)
	}
	return ev
}

const (
	probeA = `python3 -c "import m; assert m.f(1) == 2"`
	probeB = `python3 -c "import m; assert m.g() == 'x'"`
)

// Only passing probes after the turn's first edit count; a later edit
// makes the earlier ones stale, and the workspace cd is not part of the
// key.
func TestInlineProbesAfterTheFirstEdit(t *testing.T) {
	ev := probeEvents(1,
		[2]string{probeA, ""}, // before any edit: describes the old code
		[2]string{"edit", ""},
		[2]string{"cd /w && " + probeA, ""},
		[2]string{probeB, "AssertionError\n[exit code 1]"},
		[2]string{"edit", ""},
		[2]string{`python3 -c "assert 1"`, ""},
	)
	got := InlineProbes(ev, 1, "/w")
	if len(got) != 2 || got[0].Key != probeA || got[1].Key != `python3 -c "assert 1"` {
		t.Fatalf("probes = %+v", got)
	}
	if !got[0].Stale || got[1].Stale {
		t.Fatalf("stale flags = %v %v, want true false", got[0].Stale, got[1].Stale)
	}
	if got[0].Cmd != "cd /w && "+probeA {
		t.Fatalf("Cmd = %q, want the command as it ran", got[0].Cmd)
	}
	if InlineProbes(ev, 2, "/w") != nil {
		t.Fatal("another turn's probes were listed")
	}
	// A probe whose last run failed is the verdict's red check, not one
	// for the ledger.
	ev = probeEvents(1, [2]string{"edit", ""}, [2]string{probeB, ""}, [2]string{probeB, "boom\n[exit code 1]"})
	if got := InlineProbes(ev, 1, ""); len(got) != 0 {
		t.Fatalf("a probe last seen failing was listed: %+v", got)
	}
	// Background runs cannot be replayed.
	ev = probeEvents(1, [2]string{"edit", ""})
	ev = append(ev, session.Event{Type: session.TypeToolResult, Turn: 1, Seq: 9, CallID: "bg", Name: "bash", Arguments: `{"command":"python3 -c \"assert 1\"","background":true}`})
	if got := InlineProbes(ev, 1, ""); len(got) != 0 {
		t.Fatalf("a background run was listed: %+v", got)
	}
}

// fakeProbes answers probe runs from two tables: the current tree, and
// the pre-edit copy (a command that begins with the cd into it).
type fakeProbes struct {
	now, before map[string]probeOutcome
	runs        []string
}

func (f *fakeProbes) exec(_ context.Context, cmd string) probeResult {
	f.runs = append(f.runs, cmd)
	table, key := f.now, cmd
	if strings.HasPrefix(cmd, "cd '/pre'") {
		table, key = f.before, cmd[strings.Index(cmd, "\n")+1:]
	}
	o, ok := table[key]
	if !ok {
		return probeResult{}
	}
	if o == probeFail {
		return probeResult{outcome: probeFail, output: "AssertionError"}
	}
	return probeResult{outcome: o}
}

func probeAgent(f *fakeProbes, ev []session.Event) *Agent {
	mem := &session.Memory{}
	for _, e := range ev {
		mem.Append(e)
	}
	a := &Agent{Log: mem, ProbeLedger: true, probeExec: f.exec, probePreTree: func(context.Context) string { return "/pre" }}
	a.turnN, a.mutations = 1, 1
	return a
}

func (a *Agent) testProbeGate(g *probeGateState) bool {
	return a.probeGate(context.Background(), g, func(Event) {})
}

// A probe that fails on the old code and passes now is frozen; one that
// passes on both is vacuous and reaches the spec review; one that passed
// for the model and fails on a harness rerun of the same tree is neither
// frozen nor sent back (it depends on something besides the code).
func TestProbeGateClassifies(t *testing.T) {
	flaky := `python3 -c "import random; assert random.random() < 2"`
	f := &fakeProbes{
		now:    map[string]probeOutcome{probeA: probePass, probeB: probePass, flaky: probeFail},
		before: map[string]probeOutcome{probeA: probeFail, probeB: probePass},
	}
	a := probeAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}, [2]string{probeB, ""}, [2]string{flaky, ""}))
	var g probeGateState
	if a.testProbeGate(&g) {
		t.Fatal("nothing failing, yet the turn was sent back")
	}
	if len(a.probes.frozen) != 1 || a.probes.frozen[0].key != probeA {
		t.Fatalf("frozen = %+v, want only the discriminating probe", a.probes.frozen)
	}
	if len(a.probes.vacuous) != 1 || a.probes.vacuous[0] != probeB {
		t.Fatalf("vacuous = %v", a.probes.vacuous)
	}
	facts := a.probeReviewFacts()
	if !strings.Contains(facts, probeB) || strings.Contains(facts, probeA) {
		t.Fatalf("review facts = %q", facts)
	}
	// The spec review carries them.
	a.SpecReview = true
	var reviewed bool
	a.specReview(TurnOutcome{Verdict: VerdictVerified}, &reviewed, func(Event) {})
	if got := lastHarnessText(a); !strings.Contains(got, "requirement by requirement") || !strings.Contains(got, probeB) {
		t.Fatalf("spec review note = %q", got)
	}
	// No change since: the gate does not run again.
	n := len(f.runs)
	a.testProbeGate(&g)
	if len(f.runs) != n {
		t.Fatal("the gate reran without a change")
	}
	// A change: only the frozen probe is replayed; classified ones are
	// not run against the old code a second time.
	a.mutations++
	a.testProbeGate(&g)
	if got := f.runs[n:]; len(got) != 1 || got[0] != probeA {
		t.Fatalf("second gate ran %q, want only the frozen probe", got)
	}
}

// A frozen probe that a later edit broke goes back to the model, at most
// maxProbeRounds times a turn; in a later turn it was declined and is
// dropped.
func TestProbeGateSendsBackABrokenProbe(t *testing.T) {
	f := &fakeProbes{now: map[string]probeOutcome{probeA: probePass}, before: map[string]probeOutcome{probeA: probeFail}}
	a := probeAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}))
	var g probeGateState
	a.testProbeGate(&g)
	f.now[probeA] = probeFail
	for round := 1; round <= maxProbeRounds; round++ {
		a.mutations++
		if !a.testProbeGate(&g) {
			t.Fatalf("round %d: a broken frozen probe was not sent back", round)
		}
		if got := lastHarnessText(a); !strings.Contains(got, probeA) || !strings.Contains(got, "AssertionError") {
			t.Fatalf("feedback = %q", got)
		}
	}
	a.mutations++
	if a.testProbeGate(&g) {
		t.Fatal("sent back past maxProbeRounds")
	}
	// The next turn: still failing, so the model declined it.
	a.turnN++
	a.mutations++
	if a.testProbeGate(&probeGateState{}) || len(a.probes.frozen) != 0 {
		t.Fatalf("a declined probe was sent back again or kept: %+v", a.probes.frozen)
	}
}

// A probe that passed for the model, after which the turn edited again,
// and that fails now was broken by that edit: it is sent back even though
// the gate never classified it.
func TestProbeGateCatchesAStaleProbeBrokenByALaterEdit(t *testing.T) {
	f := &fakeProbes{now: map[string]probeOutcome{probeA: probeFail}}
	a := probeAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}, [2]string{"edit", ""}))
	if !a.testProbeGate(&probeGateState{}) {
		t.Fatal("a probe broken by a later edit was not sent back")
	}
	for _, r := range f.runs {
		if strings.HasPrefix(r, "cd '/pre'") {
			t.Fatal("a failing probe was run against the old code")
		}
	}
}

// Off by default, and JANETCODE_OFF=probes turns it off again.
func TestProbeGateSwitches(t *testing.T) {
	ev := probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""})
	for name, mut := range map[string]func(*Agent){
		"default off": func(a *Agent) { a.ProbeLedger = false },
		"off switch":  func(a *Agent) { a.Off = map[string]bool{"probes": true} },
		"no changes":  func(a *Agent) { a.mutations = 0 },
	} {
		f := &fakeProbes{now: map[string]probeOutcome{probeA: probePass}, before: map[string]probeOutcome{probeA: probeFail}}
		a := probeAgent(f, ev)
		mut(a)
		a.testProbeGate(&probeGateState{})
		if len(f.runs) != 0 || a.probeReviewFacts() != "" {
			t.Errorf("%s: ran %q", name, f.runs)
		}
	}
	// Without a pre-edit tree nothing is classified, so nothing is frozen
	// and nothing is called vacuous.
	f := &fakeProbes{now: map[string]probeOutcome{probeA: probePass}}
	a := probeAgent(f, ev)
	a.probePreTree = func(context.Context) string { return "" }
	a.testProbeGate(&probeGateState{})
	if len(a.probes.frozen) != 0 || len(a.probes.vacuous) != 0 {
		t.Fatalf("classified without a pre-edit tree: %+v", a.probes)
	}
}

// A pre-edit run that cannot enter the copy, never starts or times out
// says nothing about the code.
func TestJudgeProbeRun(t *testing.T) {
	for out, want := range map[string]probeOutcome{
		"ok": probePass,
		"Traceback\nAssertionError\n[exit code 1]":                  probeFail,
		"sh: cd: /tmp/x: No such file or directory\n[exit code 97]": probeInconclusive,
		"sh: python9: command not found\n[exit code 127]":           probeInconclusive,
		"[timed out after 1m0s]\n[exit code 124]":                   probeInconclusive,
	} {
		if got := judgeProbeRun(`{"command":"python3 -c 'assert 1'"}`, out).outcome; got != want {
			t.Errorf("judge(%q) = %v, want %v", out, got, want)
		}
	}
}

// End to end against a real repository, the real bash tool and the undo
// snapshot: the model adds a function and checks it inline; the probe
// fails on the old code and is frozen. A check of something the old code
// already did is vacuous and reaches the spec review.
func TestProbeLedgerEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "m.py"), []byte("def old():\n    return 1\n"), 0o644)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	newCode, _ := json.Marshal(map[string]string{"path": "m.py", "content": "def old():\n    return 1\n\ndef f(x):\n    return 2 * x\n"})
	disc, _ := json.Marshal(map[string]string{"command": "cd " + root + ` && python3 -c "import m; assert m.f(2) == 4"`})
	vac, _ := json.Marshal(map[string]string{"command": `python3 -c "import m; assert m.old() == 1"`})
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", string(newCode)),
		toolCall("t2", "bash", string(disc)),
		toolCall("t3", "bash", string(vac)),
		text("Done."),
		text("Every requirement holds."),
	}}
	a := &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, SpecReview: true, ProbeLedger: true,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
	a.Root = root
	a.Snapshots = repo
	if err := a.Run(context.Background(), "add f", nil); err != nil {
		t.Fatal(err)
	}
	if len(a.probes.frozen) != 1 || !strings.Contains(a.probes.frozen[0].key, "m.f(2)") {
		t.Fatalf("frozen = %+v, want the probe of the new function", a.probes.frozen)
	}
	if len(f.requests) != 5 {
		t.Fatalf("%d model calls, want 5", len(f.requests))
	}
	if got := diagLastText(f.requests[4]); !strings.Contains(got, "m.old() == 1") {
		t.Fatalf("the spec review was not told about the vacuous probe: %q", got)
	}
}

// lastHarnessText is the text of the last message appended to the
// conversation.
func lastHarnessText(a *Agent) string {
	if len(a.Messages) == 0 {
		return ""
	}
	return a.Messages[len(a.Messages)-1].Text()
}

// The harness reruns a probe unattended, so a call the rules would have
// put to the user is not made at all.
func TestRunProbeNeedsNoApproval(t *testing.T) {
	root := t.TempDir()
	cmd := `python3 -c "open('ran','w'); assert 1"`
	a := &Agent{Tools: tool.NewRegistry(tool.Bash{Root: root})}
	a.Root = root
	if got := a.runProbe(context.Background(), cmd, ""); got.outcome != probeInconclusive {
		t.Fatalf("outcome = %v without approval", got.outcome)
	}
	if _, err := os.Stat(filepath.Join(root, "ran")); err == nil {
		t.Fatal("the probe ran although it would have needed approval")
	}
}
