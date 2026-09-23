package textfmt

import (
	"testing"
	"time"
)

func TestCountdownRunsDown(t *testing.T) {
	prefix, wait, ok := SplitCountdown("retrying 1/2 · rate limited · in 3s")
	if !ok || prefix != "retrying 1/2 · rate limited · in " || wait != 3*time.Second {
		t.Fatalf("split = %q %v %v", prefix, wait, ok)
	}
	now := time.Now()
	deadline := now.Add(wait)
	for _, c := range []struct {
		at   time.Duration
		want string
	}{{0, "in 3s"}, {1100 * time.Millisecond, "in 2s"}, {5 * time.Second, "in 0s"}} {
		if got := Countdown(prefix, deadline, now.Add(c.at)); got != prefix[:len(prefix)-3]+c.want {
			t.Errorf("at %v: %q", c.at, got)
		}
	}
	if _, _, ok := SplitCountdown("thinking"); ok {
		t.Fatal("a phase with no wait split")
	}
	if _, w, ok := SplitCountdown("retrying 2/2 · stream cut off · in 500ms"); !ok || w != 500*time.Millisecond {
		t.Fatalf("ms wait = %v %v", w, ok)
	}
}
