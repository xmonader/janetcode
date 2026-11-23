package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func triggeredAgent(mode string, replies ...string) *Agent {
	rs := make([]*llm.Response, 0, len(replies))
	for _, r := range replies {
		rs = append(rs, text(r))
	}
	a := advisableAgent(rs...)
	a.AdvisorMode = mode
	a.trig.reset()
	return a
}

const red = "--- FAIL: TestA (0.12s)\nFAIL\n[exit code 1]"

func observeRun(a *Agent, cmd, out string) {
	a.trig.observe(a.advisorTriggeredEnabled(), "bash", []byte(`{"command":"`+cmd+`"}`), out, false)
}

func TestTriggeredRepeatedFailureFiresOnce(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "one", "two")
	ctx := context.Background()
	observeRun(a, "go test ./...", red)
	a.adviseOnTriggers(ctx, func(Event) {})
	if a.Stats().AdvisorConsults != 0 {
		t.Fatal("a single failure must not consult")
	}
	// Same failure, only the timing differs.
	observeRun(a, "go test ./...", strings.Replace(red, "0.12", "0.57", 1))
	a.adviseOnTriggers(ctx, func(Event) {})
	observeRun(a, "go test ./...", red)
	a.adviseOnTriggers(ctx, func(Event) {})
	s := a.Stats()
	if s.AdvisorConsults != 1 || s.AdvisorTriggers[triggerRepeatFail] != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestTriggeredDifferentFailureIsNotARepeat(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "x")
	observeRun(a, "go test ./...", red)
	observeRun(a, "go test ./...", "--- FAIL: TestB\n[exit code 1]")
	a.adviseOnTriggers(context.Background(), func(Event) {})
	if a.Stats().AdvisorConsults != 0 {
		t.Fatal("two different failures consulted")
	}
}

func TestTriggeredGreenToRed(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "careful")
	observeRun(a, "go test ./...", "ok\n[exit code 0]")
	observeRun(a, "go test ./...", red)
	evs, emit := collect()
	a.adviseOnTriggers(context.Background(), emit)
	s := a.Stats()
	if s.AdvisorTriggers[triggerGreenToRed] != 1 {
		t.Fatalf("stats: %+v", s)
	}
	last := a.Messages[len(a.Messages)-1]
	if !strings.Contains(last.Text(), "careful") {
		t.Fatalf("advice not injected: %q", last.Text())
	}
	found := false
	for _, e := range *evs {
		found = found || strings.Contains(e.Text, "advisor consulted (green to red)")
	}
	if !found {
		t.Fatalf("no notice names the trigger: %+v", *evs)
	}
}

func TestTriggeredEditReverted(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "rethink")
	a.turnN = 1
	a.record(Change{Turn: 1, Path: "/r/a.go", Existed: true, Before: []byte("A"), After: []byte("B")})
	a.adviseOnTriggers(context.Background(), func(Event) {})
	if a.Stats().AdvisorConsults != 0 {
		t.Fatal("an edit alone consulted")
	}
	a.record(Change{Turn: 1, Path: "/r/a.go", Existed: true, Before: []byte("B"), After: []byte("A")})
	a.adviseOnTriggers(context.Background(), func(Event) {})
	a.adviseOnTriggers(context.Background(), func(Event) {})
	if a.Stats().AdvisorTriggers[triggerRevert] != 1 || a.Stats().AdvisorConsults != 1 {
		t.Fatalf("stats: %+v", a.Stats())
	}
}

func TestRevertedPathNeedsAReturn(t *testing.T) {
	ab := []Change{
		{Path: "p", Existed: true, Before: []byte("A"), After: []byte("B")},
		{Path: "p", Existed: true, Before: []byte("B"), After: []byte("C")},
	}
	if got := revertedPath(ab); got != "" {
		t.Fatalf("progress is not a revert: %q", got)
	}
	ab = append(ab, Change{Path: "p", Existed: true, Before: []byte("C"), After: []byte("A"), Undone: true})
	if got := revertedPath(ab); got != "" {
		t.Fatalf("undone change counted: %q", got)
	}
	created := []Change{
		{Path: "n", After: []byte("x")},
		{Path: "n", Existed: true, Before: []byte("x"), After: []byte("y")},
		{Path: "n", Existed: true, Before: []byte("y"), Gone: true},
	}
	if got := revertedPath(created); got != "n" {
		t.Fatalf("deleting a file the turn created is a revert: %q", got)
	}
}

func TestTriggeredRespectsCapAndOtherModes(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "a", "b", "c")
	a.AdvisorMaxConsults = 1
	a.turnN = 1
	observeRun(a, "go test ./...", "ok\n[exit code 0]")
	observeRun(a, "go test ./...", red)
	observeRun(a, "go test ./...", red)
	a.adviseOnTriggers(context.Background(), func(Event) {})
	if a.Stats().AdvisorConsults != 1 {
		t.Fatalf("cap of one: %d consults", a.Stats().AdvisorConsults)
	}
	// Triggered does not force the always-mode consults.
	if a.advisorForcedEnabled() || a.forceAdvice(context.Background(), "gate failure", "q", func(Event) {}) {
		t.Fatal("triggered must not act as always")
	}
	for _, mode := range []string{AdvisorAlways, AdvisorOff, ""} {
		b := triggeredAgent(mode, "x")
		observeRun(b, "go test ./...", red)
		observeRun(b, "go test ./...", red)
		b.adviseOnTriggers(context.Background(), func(Event) {})
		if b.Stats().AdvisorConsults != 0 {
			t.Fatalf("mode %q consulted on a trigger", mode)
		}
	}
	c := triggeredAgent(AdvisorTriggered, "x")
	c.Off = map[string]bool{"advisor": true}
	observeRun(c, "go test ./...", red)
	observeRun(c, "go test ./...", red)
	c.adviseOnTriggers(context.Background(), func(Event) {})
	if c.Stats().AdvisorConsults != 0 {
		t.Fatal("--eval-off advisor must silence triggers")
	}
}

func TestTriggersResetEachTurn(t *testing.T) {
	a := triggeredAgent(AdvisorTriggered, "x", "y")
	observeRun(a, "go test ./...", red)
	observeRun(a, "go test ./...", red)
	a.adviseOnTriggers(context.Background(), func(Event) {})
	a.startTurn()
	a.trig.reset()
	observeRun(a, "go test ./...", red)
	observeRun(a, "go test ./...", red)
	a.adviseOnTriggers(context.Background(), func(Event) {})
	if a.Stats().AdvisorTriggers[triggerRepeatFail] != 2 {
		t.Fatalf("stats: %+v", a.Stats())
	}
}
