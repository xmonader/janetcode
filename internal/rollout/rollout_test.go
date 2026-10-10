package rollout

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// log builds a session log a step at a time: step opens a model call,
// call adds one of its tool calls.
type log struct {
	ev    []session.Event
	seq   int
	calls int
}

func (l *log) add(e session.Event) { l.seq++; e.Seq = l.seq; l.ev = append(l.ev, e) }

func (l *log) step() *log {
	l.add(session.Event{Type: session.TypeAssistant, Turn: 1, Message: &llm.Message{Role: llm.RoleAssistant}})
	return l
}

func (l *log) call(name, args string) *log {
	l.calls++
	id := fmt.Sprintf("c%d", l.calls)
	l.add(session.Event{Type: session.TypeToolCall, Turn: 1, CallID: id, Name: name, Arguments: args})
	l.add(session.Event{Type: session.TypeToolResult, Turn: 1, CallID: id, Output: "ok"})
	return l
}

func (l *log) change(path string) *log {
	l.add(session.Event{Type: session.TypeFileChange, CallID: fmt.Sprintf("c%d", l.calls), Name: path})
	return l
}

func (l *log) gate(red bool) *log {
	l.add(session.Event{Type: session.TypeGate, Name: "pytest", IsError: red, Source: session.GateVerify})
	return l
}

func (l *log) verdict(o agent.TurnOutcome) *log {
	raw, _ := json.Marshal(o)
	l.add(session.Event{Type: session.TypeVerdict, Turn: 1, Name: string(o.Verdict), Extra: raw})
	return l
}

// The critical step is the last model call that looked at something new
// before the first edit; re-reading a file already seen does not move it.
func TestCriticalStepIsLastExplorationBeforeFirstEdit(t *testing.T) {
	l := (&log{}).
		step().call("read_file", `{"path":"a.py"}`).
		step().call("bash", `{"command":"cd /w && cat b.py | head -20"}`).
		step().call("read_file", `{"path":"a.py"}`). // seen already
		step().call("edit_file", `{"path":"a.py","old_string":"x","new_string":"y"}`).
		step().call("read_file", `{"path":"c.py"}`). // after the first edit: not counted
		step()
	c := CriticalStep(l.ev)
	if c.Step != 2 || c.FirstEdit != 4 || c.Calls != 6 || !c.OK() {
		t.Fatalf("critical = %+v, want step 2, first edit 4, 6 calls", c)
	}
	if c.Explored != 2 {
		t.Fatalf("explored = %d, want 2 (a.py, b.py)", c.Explored)
	}
}

// A test run is not exploration, and the bytecode it writes is not an
// edit: neither may move the branch point.
func TestCriticalStepIgnoresTestRunsAndArtefacts(t *testing.T) {
	l := (&log{}).
		step().call("read_file", `{"path":"a.py"}`).
		step().call("bash", `{"command":"python -m pytest -q"}`).change("/w/__pycache__/a.cpython-312.pyc").
		step().call("bash", `{"command":"sed -i 's/x/y/' a.py"}`).change("/w/a.py").
		step()
	c := CriticalStep(l.ev)
	if c.Step != 1 || c.FirstEdit != 3 {
		t.Fatalf("critical = %+v, want step 1 and the sed -i as the first edit at 3", c)
	}
}

// A run whose prompt already carried the repository edits at once: no
// exploration was paid for, so there is no branch point (a branch would
// be a rerun from scratch, which the retry does not pretend to be).
func TestCriticalStepNoExplorationIsNotOK(t *testing.T) {
	l := (&log{}).step().step().
		step().call("write_file", `{"path":"a.py","content":"x"}`).
		step()
	c := CriticalStep(l.ev)
	if c.OK() || c.Step != 0 || c.FirstEdit != 3 {
		t.Fatalf("critical = %+v, want no branch point", c)
	}
}

// Exploring to the very last call leaves nothing to redo live.
func TestCriticalStepAtTheEndIsNotOK(t *testing.T) {
	l := (&log{}).step().call("read_file", `{"path":"a.py"}`).step().call("grep", `{"pattern":"Foo"}`)
	c := CriticalStep(l.ev)
	if c.Step != 2 || c.Calls != 2 || c.OK() {
		t.Fatalf("critical = %+v, want step 2 of 2 and not OK", c)
	}
}

func TestBashReads(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want int
	}{
		{"cat a.py b.py", 2},
		{"ls", 1},
		{"sed -n 1,20p a.py", 1},
		{"sed -i 's/a/b/' a.py", 0},
		{"python -m pytest", 0},
		{"cat > a.py", 0},
		{"cd /w && grep -n foo a.py | head", 1},
		{"cat a.py | grep foo", 2},
	} {
		if got := len(bashReads(tc.cmd)); got != tc.want {
			t.Errorf("bashReads(%q) = %d keys, want %d", tc.cmd, got, tc.want)
		}
	}
}

func TestBadEnd(t *testing.T) {
	green := (&log{}).gate(true).gate(false).ev
	red := (&log{}).gate(false).gate(true).ev
	for _, tc := range []struct {
		err    error
		events []session.Event
		want   End
	}{
		{nil, green, EndClean},
		{nil, red, EndGateRed},
		{fmt.Errorf("turn: %w", agent.ErrStuck), nil, EndStuck},
		{agent.ErrMaxIterations, nil, EndTurnLimit},
		{fmt.Errorf("%w: 3 made", agent.ErrToolLimit), nil, EndToolLimit},
		// A ceiling the user set on money, or a provider failure, is not
		// one a retry should spend past or can fix.
		{agent.ErrBudget, red, EndClean},
		{errors.New("connection reset"), red, EndClean},
	} {
		if got := BadEnd(tc.err, tc.events); got != tc.want {
			t.Errorf("BadEnd(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	if got := BadEndText("stuck repeating the same tool call", nil); got != EndStuck {
		t.Errorf("BadEndText(stuck) = %q", got)
	}
	if got := BadEndText("The connection dropped mid-answer.", red); got != EndClean {
		t.Errorf("BadEndText(connection) = %q, want clean", got)
	}
}

func TestWeak(t *testing.T) {
	verified := agent.TurnOutcome{Verdict: agent.VerdictVerified, Edited: 1, Ran: 1, Passed: 1, Claimed: "all tests pass"}
	stale := verified
	stale.EditedAfterCheck = true
	over := agent.TurnOutcome{Verdict: agent.VerdictUnverified, Edited: 1, Claimed: "tests pass"}
	gamed := verified
	gamed.Gaming = []string{"a_test.py — assert True (assert-true)"}
	// The usual green run: a verified verdict whose claim stands is not
	// weak, and must not buy a second sample.
	if why := Weak((&log{}).verdict(verified).ev); len(why) != 0 {
		t.Fatalf("verified run flagged weak: %v", why)
	}
	// Only the last verdict counts: an overclaim the model then fixed is
	// not weak.
	if why := Weak((&log{}).verdict(over).verdict(verified).ev); len(why) != 0 {
		t.Fatalf("a corrected overclaim flagged weak: %v", why)
	}
	for name, o := range map[string]agent.TurnOutcome{"stale": stale, "overclaimed": over, "gaming": gamed} {
		if why := Weak((&log{}).verdict(o).ev); len(why) != 1 || !strings.HasPrefix(why[0], strings.SplitN(name, " ", 2)[0][:4]) {
			t.Errorf("%s: Weak = %v", name, why)
		}
	}
	if why := Weak(nil); len(why) != 0 {
		t.Fatalf("no verdict flagged weak: %v", why)
	}
	probe := func([]session.Event) string { return "no discriminating probe" }
	if why := Weak((&log{}).verdict(verified).ev, nil, probe); len(why) != 1 || why[0] != "no discriminating probe" {
		t.Fatalf("an extra signal was not heard: %v", why)
	}
}

func TestRank(t *testing.T) {
	verified := agent.TurnOutcome{Verdict: agent.VerdictVerified, Edited: 1, Ran: 1, Passed: 1}
	clean := Candidate{Name: "first", Verdict: verified, HasVerdict: true, Waste: 100}
	stuck := Candidate{Name: "first", End: EndStuck, Failed: true, Waste: 0}
	if r := Rank([]Candidate{stuck, withName(clean, "branch")}, nil); r.Winner != 1 || r.By != "end" {
		t.Fatalf("a clean branch lost to a stuck first attempt: %+v", r)
	}
	// A tie goes to the first attempt: its tree is already in place.
	if r := Rank([]Candidate{clean, withName(clean, "sample")}, nil); r.Winner != 0 || r.By != "" {
		t.Fatalf("tie = %+v, want the first attempt and no deciding key", r)
	}
	// Waste only breaks a tie the checks left.
	lean := withName(clean, "sample")
	lean.Waste = 10
	if r := Rank([]Candidate{clean, lean}, nil); r.Winner != 1 || r.By != "waste" {
		t.Fatalf("less waste did not win a tie: %+v", r)
	}
	gamed := lean
	gamed.Gaming = 1
	if r := Rank([]Candidate{clean, gamed}, nil); r.Winner != 0 || r.By != "gaming" {
		t.Fatalf("a gaming signal was outweighed by waste: %+v", r)
	}
	// The ledgers' slot ranks ahead of gaming and waste once filled.
	probed := lean
	probed.Extra = map[string]int{ExtraProbeFailures: 2}
	if r := Rank([]Candidate{clean, probed}, nil); r.Winner != 0 || r.By != "probes" {
		t.Fatalf("frozen-probe failures did not decide: %+v", r)
	}
	red := withName(clean, "sample")
	red.GateRed = true
	red.Waste = 0
	if r := Rank([]Candidate{clean, red}, nil); r.Winner != 0 || r.By != "gate" {
		t.Fatalf("a red gate won on waste: %+v", r)
	}
}

func withName(c Candidate, name string) Candidate { c.Name = name; return c }

func TestNewCandidateWaste(t *testing.T) {
	l := (&log{}).step().call("grep", `{"pattern":"x"}`).call("grep", `{"pattern":"x"}`).call("grep", `{"pattern":"x"}`).call("read_file", `{"path":"a"}`)
	c := NewCandidate("first", l.ev, nil)
	if c.Waste != 750 {
		t.Fatalf("waste = %d‰, want 750 (three looped greps of four calls)", c.Waste)
	}
}

func TestBranchNote(t *testing.T) {
	l := (&log{}).
		step().call("read_file", `{"path":"/w/a.py"}`).
		step().call("edit_file", `{"path":"/w/a.py"}`).
		step().call("bash", `{"command":"pytest"}`)
	c := CriticalStep(l.ev)
	n := BranchNote(l.ev, c, EndStuck, "/w")
	for _, want := range []string{"made 2 tool calls, 1 of them edits to a.py", "stuck repeating the same bash call", "different approach"} {
		if !strings.Contains(n, want) {
			t.Errorf("note %q lacks %q", n, want)
		}
	}
	if strings.Contains(n, "\n") || len(n) > noteBudget {
		t.Errorf("note is not one bounded line: %q", n)
	}
}

// The pre-screen counts what each trigger would have fired on and how
// often that was a hidden-test failure; a trigger that never fired has
// no precision rather than zero.
func TestRunScreen(t *testing.T) {
	verified := agent.TurnOutcome{Verdict: agent.VerdictVerified, Edited: 1, Ran: 1, Passed: 1}
	over := agent.TurnOutcome{Verdict: agent.VerdictUnverified, Edited: 1, Claimed: "tests pass"}
	explored := (&log{}).step().call("read_file", `{"path":"a"}`).step().call("edit_file", `{"path":"a"}`).step().ev
	runs := []Run{
		{Name: "stuck", Events: explored, Err: agent.ErrStuck.Error(), Pass: false},
		{Name: "dropped", Events: explored, Err: "connection reset", Pass: false},
		{Name: "weak-fail", Events: (&log{}).verdict(over).ev, Pass: false},
		{Name: "weak-pass", Events: (&log{}).verdict(over).ev, Pass: true},
		{Name: "green", Events: (&log{}).verdict(verified).ev, Pass: true},
		{Name: "green-wrong", Events: (&log{}).verdict(verified).ev, Pass: false},
	}
	s := RunScreen(runs)
	if len(s.Bad) != 1 || !s.Bad[0].Critical.OK() || len(s.Other) != 3 {
		t.Fatalf("bad %+v, other %+v", s.Bad, s.Other)
	}
	if s.Green != 4 || s.GreenFail != 2 || s.Triggered != 2 || s.TriggeredFail != 1 {
		t.Fatalf("screen = %+v", s)
	}
	if p, ok := s.Precision(); !ok || p != 0.5 {
		t.Fatalf("precision = %v %v, want 0.5", p, ok)
	}
	if _, ok := RunScreen(runs[4:]).Precision(); ok {
		t.Fatal("a trigger that never fired has a precision")
	}
	if !strings.Contains(s.Text(), "precision: 1/2 = 50%") {
		t.Fatalf("text:\n%s", s.Text())
	}
}
