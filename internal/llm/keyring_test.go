package llm

import (
	"testing"
	"time"
)

func TestKeyRingBreakers(t *testing.T) {
	r := NewKeyRing([]string{"a", " b ", "a", "c"})
	if r.Len() != 3 {
		t.Fatalf("len = %d", r.Len())
	}
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }

	k, i := r.Current()
	if k != "a" || i != 0 {
		t.Fatalf("first = %s/%d", k, i)
	}
	// A rate limit moves on to b; a is back after its backoff.
	if !r.Fail(0, FailRateLimit) {
		t.Fatal("b should be available")
	}
	if k, _ := r.Current(); k != "b" {
		t.Fatalf("after rate limit: %s", k)
	}
	now = now.Add(20 * time.Second)
	r.OK(1)
	if k, _ := r.Current(); k != "b" {
		t.Fatalf("healthy current key should stay: %s", k)
	}
	// An auth failure keeps a key out for an hour even after the others fail.
	r.Fail(1, FailAuth)
	if k, _ := r.Current(); k != "c" {
		t.Fatalf("after auth failure: %s", k)
	}
	r.Fail(2, FailRateLimit) // c out for 15s; a is healthy again (20s passed)
	if k, _ := r.Current(); k != "a" {
		t.Fatalf("expected a: %s", k)
	}
	// Everything tripped: the soonest to recover is used and Fail reports
	// no alternative.
	if r.Fail(0, FailQuota) {
		t.Fatal("no healthy key should remain")
	}
	if r.Healthy() != 0 {
		t.Fatalf("healthy = %d", r.Healthy())
	}
	if k, _ := r.Current(); k != "c" {
		t.Fatalf("soonest to recover should be c, got %s", k)
	}
	now = now.Add(2 * time.Hour)
	if r.Healthy() != 3 {
		t.Fatalf("healthy after penalties expire = %d", r.Healthy())
	}
	// Growing backoff for repeated rate limits, capped.
	r2 := NewKeyRing([]string{"x"})
	r2.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		r2.Fail(0, FailRateLimit)
	}
	if got := r2.until[0].Sub(now); got != maxRatePenalty {
		t.Fatalf("penalty = %s", got)
	}
	if NewKeyRing(nil).Len() != 1 {
		t.Fatal("empty ring should hold one blank key")
	}
	if ClassifyFailure(429, "insufficient credits") != FailQuota || ClassifyFailure(403, "") != FailAuth || ClassifyFailure(429, "slow down") != FailRateLimit {
		t.Fatal("classification")
	}
}
