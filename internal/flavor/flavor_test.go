package flavor

import (
	"testing"
	"time"
)

func TestPhraseIsSeededAndStable(t *testing.T) {
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	a := Phrase(started, started)
	b := Phrase(started, started)
	if a == "" || a != b {
		t.Fatalf("Phrase = %q, %q", a, b)
	}
	if !Known(a) {
		t.Fatalf("%q is not in the seeded list", a)
	}
}

func TestPhraseRotatesWithElapsedTime(t *testing.T) {
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	first := Phrase(started, started)
	later := Phrase(started.Add(Interval), started)
	if later == first {
		t.Fatalf("phrase did not rotate after %s: still %q", Interval, first)
	}
	if !Known(later) {
		t.Fatalf("rotated phrase %q is not in the seeded list", later)
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
		t.Fatalf("dressed phase %q is not a seeded flavor phrase", got)
	}
}

func TestPhraseMixesVerbsAndWitty(t *testing.T) {
	if len(witty) < 50 {
		t.Fatalf("witty list too small: %d", len(witty))
	}
	if !Known("Reticulating splines...") || !Known("Don't panic…") || !Known("Engage.") {
		t.Fatal("seeded witty lines are not Known")
	}
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	sawVerb, sawWitty := false, false
	n := len(verbs) + len(witty)
	for i := 0; i < n; i++ {
		got := Phrase(started.Add(time.Duration(i)*Interval), started)
		if !Known(got) {
			t.Fatalf("Phrase %d = %q is not Known", i, got)
		}
		key := trimEllipsis(got)
		for _, v := range verbs {
			if v == key {
				sawVerb = true
			}
		}
		for _, p := range witty {
			if got == dress(p) {
				sawWitty = true
			}
		}
	}
	if !sawVerb || !sawWitty {
		t.Fatalf("pool missed a source: verb=%v witty=%v", sawVerb, sawWitty)
	}
}

func TestDressNormalizesEllipsis(t *testing.T) {
	if got := dress("Reticulating splines..."); got != "Reticulating splines…" {
		t.Fatalf("ellipsis = %q", got)
	}
	if got := dress("Engage."); got != "Engage." {
		t.Fatalf("period = %q", got)
	}
	if got := dress("Loading... Do a barrel roll!"); got != "Loading... Do a barrel roll!" {
		t.Fatalf("bang = %q", got)
	}
	if got := dress("Moonwalking"); got != "Moonwalking…" {
		t.Fatalf("verb = %q", got)
	}
}
