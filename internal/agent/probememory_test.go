package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
	"github.com/xmonader/janetcode/internal/janetref"
	"github.com/xmonader/janetcode/internal/session"
)

// probeMemAgent is probeAgent with the probe memory on in repo and the
// ledger itself left to it.
func probeMemAgent(f *fakeProbes, ev []session.Event, repo string) *Agent {
	a := probeAgent(f, ev)
	a.ProbeLedger, a.ProbeMemory, a.probeMemRepoDir = false, true, repo
	return a
}

func probeMemRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitxtest.InitRepo(t, dir)
	return dir
}

// A discriminating probe is written to the memory; a vacuous one is not.
func TestProbeMemoryRemembersDiscriminatingProbes(t *testing.T) {
	repo := probeMemRepo(t)
	f := &fakeProbes{
		now:    map[string]probeOutcome{probeA: probePass, probeB: probePass},
		before: map[string]probeOutcome{probeA: probeFail, probeB: probePass},
	}
	a := probeMemAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}, [2]string{probeB, ""}), repo)
	if !a.ProbeLedgerOn() {
		t.Fatal("probe_memory did not turn the ledger on")
	}
	if a.testProbeGate(&probeGateState{}) {
		t.Fatal("sent back with nothing failing")
	}
	got, _, err := janetref.LoadProbes(context.Background(), repo)
	if err != nil || len(got) != 1 || got[0].Cmd != probeA {
		t.Fatalf("memory = %+v, %v", got, err)
	}
}

// A later session arms a remembered probe that holds on the code as it
// was before its turn and sends the turn back when the turn breaks it,
// saying where the probe came from; one that already failed before the
// turn is left out. A probe the model leaves failing into the next turn
// is retired from the memory.
func TestProbeMemoryArmsReplaysAndRetires(t *testing.T) {
	ctx := context.Background()
	repo := probeMemRepo(t)
	if _, err := janetref.AddProbes(ctx, repo, []janetref.StoredProbe{
		{Cmd: probeA, Why: "f doubles its argument", Session: "s-old"},
		{Cmd: probeB, Why: "g returns x", Session: "s-old"},
	}); err != nil {
		t.Fatal(err)
	}
	f := &fakeProbes{
		now:    map[string]probeOutcome{probeA: probeFail, probeB: probeFail},
		before: map[string]probeOutcome{probeA: probePass, probeB: probeFail},
	}
	a := probeMemAgent(f, probeEvents(1, [2]string{"edit", ""}), repo)
	if !a.testProbeGate(&probeGateState{}) {
		t.Fatal("a broken remembered probe did not send the turn back")
	}
	if len(a.probes.frozen) != 1 || a.probes.frozen[0].key != probeA {
		t.Fatalf("armed = %+v", a.probes.frozen)
	}
	fb := lastHarnessText(a)
	if !strings.Contains(fb, "remembered from an earlier session s-old, which made it pass: f doubles its argument") {
		t.Fatalf("feedback = %s", fb)
	}
	// The memory is read once a session.
	runs := len(f.runs)
	a.mutations++
	a.testProbeGate(&probeGateState{})
	for _, r := range f.runs[runs:] {
		if strings.Contains(r, probeB) {
			t.Fatal("the memory was armed again")
		}
	}
	// Next turn, still failing: declined, retired from the ledger and
	// the memory.
	a.turnN, a.mutations = 2, a.mutations+1
	a.testProbeGate(&probeGateState{})
	if len(a.probes.frozen) != 0 {
		t.Fatalf("still frozen: %+v", a.probes.frozen)
	}
	got, _, _ := janetref.LoadProbes(ctx, repo)
	if len(got) != 1 || got[0].Cmd != probeB {
		t.Fatalf("memory after retiring = %+v", got)
	}
}

// A probe of the session's own that fails is not labelled as remembered.
func TestProbeMemoryOwnProbeIsNotLabelled(t *testing.T) {
	repo := probeMemRepo(t)
	f := &fakeProbes{
		now:    map[string]probeOutcome{probeA: probePass},
		before: map[string]probeOutcome{probeA: probeFail},
	}
	a := probeMemAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}), repo)
	a.testProbeGate(&probeGateState{})
	if a.probeMemNote(probeA) != "" {
		t.Fatal("this session's own probe was labelled as remembered")
	}
}

func TestProbeMemorySwitches(t *testing.T) {
	for _, off := range []string{"probememory", "probes"} {
		repo := probeMemRepo(t)
		janetref.AddProbes(context.Background(), repo, []janetref.StoredProbe{{Cmd: probeB}})
		f := &fakeProbes{
			now:    map[string]probeOutcome{probeA: probePass, probeB: probeFail},
			before: map[string]probeOutcome{probeA: probeFail, probeB: probePass},
		}
		a := probeMemAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}), repo)
		a.Off = map[string]bool{off: true}
		if a.ProbeMemoryOn() || a.testProbeGate(&probeGateState{}) || len(f.runs) != 0 {
			t.Fatalf("%s: ran %v", off, f.runs)
		}
		if got, _, _ := janetref.LoadProbes(context.Background(), repo); len(got) != 1 {
			t.Fatalf("%s: memory written: %+v", off, got)
		}
	}
	// With the ledger on and the memory off, the ledger runs and nothing
	// reaches the repository.
	repo := probeMemRepo(t)
	f := &fakeProbes{now: map[string]probeOutcome{probeA: probePass}, before: map[string]probeOutcome{probeA: probeFail}}
	a := probeAgent(f, probeEvents(1, [2]string{"edit", ""}, [2]string{probeA, ""}))
	a.probeMemRepoDir = repo
	a.testProbeGate(&probeGateState{})
	if len(a.probes.frozen) != 1 {
		t.Fatal("the ledger did not run")
	}
	if tip, _ := janetref.Tip(context.Background(), repo, janetref.ProbesRef); tip != "" {
		t.Fatal("the memory was written while off")
	}
}
