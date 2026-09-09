package llm

import (
	"context"
	"testing"
	"time"
)

func TestPacerCostsNothingUntilRefused(t *testing.T) {
	var p Pacer
	start := time.Now()
	for range 5 {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("an unrefused session waited %s", d)
	}
	if p.Gap() != 0 {
		t.Fatalf("gap = %s, want 0", p.Gap())
	}
}

func TestPacerWidensAndRecovers(t *testing.T) {
	p := Pacer{Base: 10 * time.Millisecond, Max: 40 * time.Millisecond, Ease: 4 * time.Millisecond, Quiet: 20 * time.Millisecond}
	p.Refused()
	if p.Gap() != 10*time.Millisecond {
		t.Fatalf("first refusal gap = %s", p.Gap())
	}
	p.Refused()
	p.Refused()
	if p.Gap() != 40*time.Millisecond {
		t.Fatalf("gap should double to the cap, got %s", p.Gap())
	}
	p.Refused()
	if p.Gap() != 40*time.Millisecond {
		t.Fatalf("gap must not exceed Max, got %s", p.Gap())
	}
	// A success right after a refusal is the retry landing, not recovery.
	p.Served()
	if p.Gap() != 40*time.Millisecond {
		t.Fatalf("recovery began too early: %s", p.Gap())
	}
	time.Sleep(25 * time.Millisecond)
	p.Served()
	if p.Gap() != 36*time.Millisecond {
		t.Fatalf("gap = %s, want one ease step down", p.Gap())
	}
}

func TestPacerSpacesCallsAndHonoursCancel(t *testing.T) {
	p := Pacer{Base: 60 * time.Millisecond}
	p.Refused()
	if err := p.Wait(context.Background()); err != nil { // first call: no wait
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("second call waited only %s", d)
	}
	// A user interrupting is not made to sit through the gap.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Wait(ctx); err == nil {
		t.Fatal("a cancelled context should return promptly with an error")
	}
}
