package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
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

// Two calls traded back and forth get called out even when both succeed,
// but only once an edit is out of the picture: an edit-and-check rhythm
// is how the work is meant to go.
func TestLoopDetectorFindsAlternation(t *testing.T) {
	var d loopDetector
	d.reset()
	// The inputs vary while what the calls are about does not, so this
	// is a loop the identical-call counter cannot see.
	n := 0
	ping := func() (string, bool) {
		n++
		return d.record("grep", []byte(fmt.Sprintf(`{"pattern":"alpha","limit":%d}`, n)), false, false)
	}
	pong := func() (string, bool) {
		return d.record("list_dir", []byte(fmt.Sprintf(`{"path":"xdir","offset":%d}`, n)), false, false)
	}
	var note string
	var stop bool
	for i := 0; i < alternateNudgeAt-1; i++ {
		ping()
		if note, stop = pong(); note != "" || stop {
			t.Fatalf("round %d: note=%q stop=%v", i, note, stop)
		}
	}
	ping()
	note, stop = pong()
	if stop || !strings.Contains(note, "gone back and forth") {
		t.Fatalf("nudge: note=%q stop=%v", note, stop)
	}
	for i := alternateNudgeAt; i < alternateStopAt-1; i++ {
		ping()
		pong()
	}
	ping()
	if _, stop = pong(); !stop || !strings.Contains(d.reason, "alternated between two calls") {
		t.Fatalf("stop=%v reason=%q", stop, d.reason)
	}
	// An edit in the pair is progress, not a loop.
	d.reset()
	for i := 0; i < alternateStopAt+2; i++ {
		d.record("edit_file", []byte(`{"path":"a.go"}`), false, true)
		if note, stop := d.record("bash", []byte(`{"command":"go build ./..."}`), false, false); note != "" || stop {
			t.Fatalf("edit round %d: note=%q stop=%v", i, note, stop)
		}
	}
}

// The same file read over and over, with other calls in between, is a
// model that has lost what it already read.
func TestLoopDetectorFindsReadThrash(t *testing.T) {
	var d loopDetector
	d.reset()
	read := func(off int) (string, bool) {
		return d.record("read_file", []byte(fmt.Sprintf(`{"path":"big.go","offset":%d}`, off)), false, false)
	}
	other := func(i int) { d.record("grep", []byte(fmt.Sprintf(`{"pattern":"p%d"}`, i)), false, false) }
	var note string
	var stop bool
	for i := 0; i < thrashNudgeAt-1; i++ {
		if note, stop = read(i); note != "" || stop {
			t.Fatalf("read %d: note=%q stop=%v", i, note, stop)
		}
		other(i)
	}
	note, stop = read(thrashNudgeAt)
	if stop || !strings.Contains(note, "read big.go 5 times") {
		t.Fatalf("nudge: note=%q stop=%v", note, stop)
	}
	for i := thrashNudgeAt; i < thrashStopAt-1; i++ {
		read(i)
	}
	if _, stop = read(thrashStopAt); !stop || !strings.Contains(d.reason, "read big.go 8 times") {
		t.Fatalf("stop=%v reason=%q", stop, d.reason)
	}
	// Reading different files is not thrash.
	d.reset()
	for i := 0; i < 12; i++ {
		if note, stop := d.record("read_file", []byte(fmt.Sprintf(`{"path":"f%d.go"}`, i)), false, false); note != "" || stop {
			t.Fatalf("file %d: note=%q stop=%v", i, note, stop)
		}
	}
}

// However varied the calls, a turn that makes a hundred of them is not
// working, it is running.
func TestLoopDetectorCapsCallsPerTurn(t *testing.T) {
	var d loopDetector
	d.reset()
	for i := 1; i < maxTurnCalls; i++ {
		if _, stop := d.record("grep", []byte(fmt.Sprintf(`{"pattern":"p%d"}`, i)), false, false); stop {
			t.Fatalf("stopped early at call %d: %s", i, d.reason)
		}
	}
	if _, stop := d.record("grep", []byte(`{"pattern":"last"}`), false, false); !stop || !strings.Contains(d.reason, "100 tool calls") {
		t.Fatalf("stop=%v reason=%q", stop, d.reason)
	}
}

func TestLoopDetectorFindsRepeatedAssistantText(t *testing.T) {
	var d loopDetector
	d.reset()
	msg := "I will now look at the configuration file and work out what changed."
	if note, stop := d.recordText(msg); note != "" || stop {
		t.Fatalf("first: note=%q stop=%v", note, stop)
	}
	note, stop := d.recordText(msg)
	if stop || !strings.Contains(note, "written this same message 2 times") {
		t.Fatalf("second: note=%q stop=%v", note, stop)
	}
	if _, stop := d.recordText(msg); !stop || !strings.Contains(d.reason, "same message 3 times") {
		t.Fatalf("third: stop=%v reason=%q", stop, d.reason)
	}
	// A short acknowledgement repeated is not an answer repeated.
	d.reset()
	for i := 0; i < 5; i++ {
		if note, stop := d.recordText("ok"); note != "" || stop {
			t.Fatalf("short text %d: note=%q stop=%v", i, note, stop)
		}
	}
}

// Wiring: a model that answers with the same text every step ends the
// turn rather than being asked forever.
func TestTurnStopsOnRepeatedAssistantText(t *testing.T) {
	same := "I am going to read the configuration file and work out what changed here."
	var responses []*llm.Response
	for i := 0; i < 4; i++ {
		r := toolCall(fmt.Sprint(i), "read_file", fmt.Sprintf(`{"path":"a.txt","offset":%d}`, i))
		r.Message.Blocks = append([]llm.Block{{Type: llm.BlockText, Text: same}}, r.Message.Blocks...)
		responses = append(responses, r)
	}
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, nil)
	err := a.Run(context.Background(), "go", func(Event) {})
	if !errors.Is(err, ErrStuck) {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != sameTextStopAt {
		t.Fatalf("%d model calls, want %d", len(f.requests), sameTextStopAt)
	}
}
