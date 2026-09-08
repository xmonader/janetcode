package agent

import (
	"strings"
	"testing"
)

func TestLoopDetectorFindsFailingCycles(t *testing.T) {
	var d loopDetector
	d.reset()
	edit := func(i int) (string, bool) {
		return d.record("edit_file", []byte(`{"path":"a.go","old_string":"v`+string(rune('0'+i))+`","new_string":"w"}`), false, true)
	}
	test := func() (string, bool) {
		return d.record("bash", []byte(`{"command":"go test ./..."}`), true, false)
	}
	// Two rounds are normal work.
	for i := 0; i < 2; i++ {
		edit(i)
		if note, stop := test(); note != "" || stop {
			t.Fatalf("round %d: note=%q stop=%v", i, note, stop)
		}
	}
	// The third round is called out, naming the loop.
	edit(2)
	note, stop := test()
	if stop || !strings.Contains(note, "gone round the same loop 3 times") || !strings.Contains(note, `edit_file "a.go" → bash "go test ./..." fails`) {
		t.Fatalf("round 3: note=%q stop=%v", note, stop)
	}
	for i := 3; i < 5; i++ {
		edit(i)
		test()
	}
	edit(5)
	if _, stop := test(); !stop || !strings.Contains(d.reason, "6 times") {
		t.Fatalf("round 6: stop=%v reason=%q", stop, d.reason)
	}
	// A converging loop (the test passes) is never a cycle, and a repeated
	// pair of successful reads is not either.
	d.reset()
	for i := 0; i < 8; i++ {
		edit(i)
		if note, stop := d.record("bash", []byte(`{"command":"go test ./..."}`), false, false); note != "" || stop {
			t.Fatalf("passing round %d: note=%q stop=%v", i, note, stop)
		}
	}
	d.reset()
	for i := 0; i < 4; i++ {
		d.record("read_file", []byte(`{"path":"a.go"}`), false, false)
		d.record("read_file", []byte(`{"path":"b.go"}`), false, false)
	}
	if period, _ := d.cycle(); period != 0 {
		t.Fatalf("successful reads counted as a cycle (period %d)", period)
	}
	// Longer periods are found too: read, edit, test fails.
	d.reset()
	for i := 0; i < 3; i++ {
		d.record("read_file", []byte(`{"path":"a.go"}`), false, false)
		edit(i)
		test()
	}
	if period, reps := d.cycle(); period != 3 || reps != 3 {
		t.Fatalf("period=%d reps=%d", period, reps)
	}
}
