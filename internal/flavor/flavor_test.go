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

// The opening phrase used to be picked with int arithmetic on a Unix
// time in milliseconds, which does not fit in a 32-bit int: on a 32-bit
// build the truncation went negative and was clamped, so every turn
// opened on the first phrase in the pool. The offset is computed in
// int64 and wrapped, so it is the same on every platform.
func TestStartOffsetUsesTheWholeTimestamp(t *testing.T) {
	n := len(pool)
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	if got, want := startOffset(started, n), int(started.UnixMilli()%int64(n)); got != want {
		t.Fatalf("startOffset = %d, want %d", got, want)
	}
	// Times before the epoch wrap into range rather than clamping.
	old := time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := startOffset(old, n); got < 0 || got >= n {
		t.Fatalf("startOffset(pre-epoch) = %d, out of range", got)
	}
	// Turns that start at different moments do not all open on the same
	// phrase, which is the whole point of seeding from the start time.
	seen := map[string]bool{}
	for i := range 20 {
		at := started.Add(time.Duration(i) * time.Second)
		seen[Phrase(at, at)] = true
	}
	if len(seen) < 5 {
		t.Fatalf("20 start times produced only %d opening phrases", len(seen))
	}
}

// A turn left open for a very long time still names a phrase in the
// pool: the step count is reduced before it is added, so the sum cannot
// overflow into a negative index.
func TestAVeryLongWaitStaysInThePool(t *testing.T) {
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{time.Hour, 24 * 365 * time.Hour, 1<<62 - 1} {
		got := Phrase(started.Add(d), started)
		if !Known(got) {
			t.Fatalf("after %s: %q is not a seeded phrase", d, got)
		}
	}
	// A clock that went backwards does not panic or index negatively.
	if got := Phrase(started.Add(-time.Hour), started); !Known(got) {
		t.Fatalf("backwards clock: %q", got)
	}
}

func TestKnownAcceptsEverySeededSpelling(t *testing.T) {
	for _, p := range pool {
		for _, spelling := range []string{p, dress(p), trimEllipsis(p), "  " + p + " "} {
			if !Known(spelling) {
				t.Fatalf("Known(%q) = false for seeded phrase %q", spelling, p)
			}
		}
	}
	for _, s := range []string{"", "running bash", "Thinking", "Reticulating splinez"} {
		if Known(s) {
			t.Errorf("Known(%q) = true", s)
		}
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
