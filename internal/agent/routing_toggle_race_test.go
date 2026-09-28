package agent

import (
	"sync"
	"testing"
)

// /routing is typed while a turn runs, and the turn goroutine reads the
// other Off switches (loops, verify, escalate, ...) without a lock. The
// toggle must therefore never write the Off map those reads look in: a
// map written on one goroutine and read on another is a data race the
// runtime can end the process over ("concurrent map read and map
// write"). Run under -race.
func TestRoutingToggleDoesNotRaceOffReads(t *testing.T) {
	a := &Agent{Off: map[string]bool{"sleep": true}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			a.ToggleOff("routing")
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			_ = a.Off["loops"]
		}
	}()
	wg.Wait()
	if !a.Off["sleep"] {
		t.Fatal("the switches set at build time must survive a toggle")
	}
	if a.RoutingOff() {
		t.Fatal("an even number of toggles leaves routing on")
	}
	if !a.ToggleOff("routing") || !a.RoutingOff() {
		t.Fatal("an odd number of toggles vetoes routing")
	}
}

// A session started with routing switched off (--off routing) is lifted
// by the first /routing, as it always was.
func TestRoutingToggleLiftsABuildTimeVeto(t *testing.T) {
	a := &Agent{Off: map[string]bool{"routing": true}}
	if !a.RoutingOff() {
		t.Fatal("--off routing vetoes routing")
	}
	if a.ToggleOff("routing") || a.RoutingOff() {
		t.Fatal("the first toggle lifts a build-time veto")
	}
}
