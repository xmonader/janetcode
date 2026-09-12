package flavor

import (
	"testing"
	"time"
)

func TestVerbIsSeededAndStable(t *testing.T) {
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	a := Verb(started, started)
	b := Verb(started, started)
	if a == "" || a != b {
		t.Fatalf("Verb = %q, %q", a, b)
	}
	if !Known(a) {
		t.Fatalf("%q is not in the seeded list", a)
	}
}

func TestVerbRotatesWithElapsedTime(t *testing.T) {
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	first := Verb(started, started)
	later := Verb(started.Add(Interval), started)
	if later == first {
		t.Fatalf("verb did not rotate after %s: still %q", Interval, first)
	}
	if !Known(later) {
		t.Fatalf("rotated verb %q is not in the seeded list", later)
	}
}

func TestForPhaseLeavesRealWaitsAlone(t *testing.T) {
	now := time.Unix(0, 0)
	for _, phase := range []string{
		"running bash",
		"writing",
		"waiting on subagent",
		"retrying 1/2 · rate limited · in 6s",
		"compacting context",
		"cancelling",
		"",
	} {
		if got := ForPhase(phase, now, now); got != phase {
			t.Errorf("ForPhase(%q) = %q", phase, got)
		}
	}
}

func TestForPhaseDressesThinking(t *testing.T) {
	now := time.Unix(1, 0)
	got := ForPhase("thinking", now, now)
	if got == "thinking" || got == "thinking…" {
		t.Fatalf("thinking was not dressed: %q", got)
	}
	if !Known(got) {
		t.Fatalf("dressed phase %q is not a seeded verb", got)
	}
}
