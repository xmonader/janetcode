package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
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

// The reply that trips the repeated-text stop carries tool calls of its
// own, and it is already in the history when the turn ends. Left
// unanswered, every later request carried a tool_use with no tool_result,
// which every provider refuses: the session could not take another turn.
func TestRepeatedTextStopLeavesAPairedHistory(t *testing.T) {
	same := "I am going to read the configuration file and work out what changed here."
	var responses []*llm.Response
	for i := 0; i < sameTextStopAt; i++ {
		r := toolCall(fmt.Sprint(i), "read_file", fmt.Sprintf(`{"path":"a.txt","offset":%d}`, i))
		r.Message.Blocks = append([]llm.Block{{Type: llm.BlockText, Text: same}}, r.Message.Blocks...)
		responses = append(responses, r)
	}
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, nil)
	mem := &session.Memory{}
	a.Log = mem
	if err := a.Run(context.Background(), "go", func(Event) {}); !errors.Is(err, ErrStuck) {
		t.Fatalf("err = %v", err)
	}
	assertPaired(t, a.Messages)
	assertLogFoldsToHistory(t, a, mem)
}

// freshBigTool returns a different long output on every call, so each
// result is archived and the model sees a stub carrying its own sha256
// and archive id.
type freshBigTool struct{ n int }

func (*freshBigTool) Name() string            { return "big" }
func (*freshBigTool) Description() string     { return "produces a lot of output" }
func (*freshBigTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*freshBigTool) Mutating() bool          { return false }
func (b *freshBigTool) Run(context.Context, json.RawMessage) (string, error) {
	b.n++
	return fmt.Sprintf("run %d\n", b.n) + strings.Repeat("x", 40*1024), nil
}

// Loop detection keys on the call, never on the result, so a repeated
// call whose earlier results were replaced by stubs is still a repeat.
// The stub's sha256 identifies the output it stands for, which is a
// different question, and feeding it into the comparison would break
// this: the outputs of the identical call differ run to run (a timestamp,
// a line count, a fresh archive id) while the call is the same, and that
// is what "not making progress" means.
func TestStubbedResultsDoNotHideARepeatedCall(t *testing.T) {
	same := func() *llm.Response { return toolCall("t", "big", `{}`) }
	f := &fake{responses: []*llm.Response{same(), same(), same(), same(), same(), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(&freshBigTool{})
	a.Archive = NewArchive(t.TempDir())
	evs, emit := collect()
	err := a.Run(context.Background(), "call it over and over", emit)
	if !errors.Is(err, ErrStuck) {
		t.Fatalf("err = %v, want ErrStuck after %d identical calls", err, loopStopAt)
	}
	// Each result really was stubbed, with a digest of its own: this is
	// the condition the test is about, not an accident of size.
	digests := map[string]bool{}
	stubs := 0
	for _, e := range *evs {
		if e.Kind != EvToolResult || !strings.HasPrefix(e.Output, "[output is ") {
			continue
		}
		stubs++
		if m := regexp.MustCompile(`sha256 ([0-9a-f]+)`).FindStringSubmatch(e.Output); m != nil {
			digests[m[1]] = true
		}
	}
	if stubs < loopStopAt-1 || len(digests) != stubs {
		t.Fatalf("%d stubbed results with %d distinct digests", stubs, len(digests))
	}
	// The nudge came first, at the third identical call, and named the
	// tool rather than the output.
	var nudged bool
	for _, e := range *evs {
		if e.Kind == EvToolResult && strings.Contains(e.Output, "identical big call this turn") {
			nudged = true
		}
	}
	if !nudged {
		t.Error("the ladder did not nudge before it stopped the turn")
	}
}

// The detector itself: the same call with a different result every time
// is still the same call.
func TestLoopDetectorIgnoresTheResultText(t *testing.T) {
	var d loopDetector
	d.reset()
	for i := 1; i < loopStopAt; i++ {
		if _, stop := d.record("grep", []byte(`{"pattern":"TODO"}`), false, false); stop {
			t.Fatalf("stopped at call %d", i)
		}
	}
	if _, stop := d.record("grep", []byte(`{"pattern":"TODO"}`), false, false); !stop {
		t.Fatalf("call %d did not stop the turn: reason = %q", loopStopAt, d.reason)
	}
}

// The loop detector's stop is carried to the end of the tool step in
// a.stuck. A step that ended on an error first — here an approver that
// could not answer, in the batch whose read tripped the detector — left it
// set, and the next turn, which had repeated nothing, ended with ErrStuck
// after its very first tool call.
func TestStuckFlagDoesNotOutliveItsTurn(t *testing.T) {
	boom := errors.New("the front-end went away")
	read := `{"path":"a.txt"}`
	var responses []*llm.Response
	for i := 1; i < loopStopAt; i++ {
		responses = append(responses, toolCall(fmt.Sprint(i), "read_file", read))
	}
	responses = append(responses, &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "r", Name: "read_file", Input: []byte(read)},
			{Type: llm.BlockToolUse, ID: "w", Name: "write_file", Input: []byte(`{"path":"b.txt","content":"x\n"}`)},
		}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	})
	// The next turn: one ordinary call, then an answer.
	responses = append(responses, toolCall("n", "read_file", `{"path":"a.txt","offset":1}`), text("done"))
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, brokenApprover{err: boom})
	if err := a.Run(context.Background(), "go", nil); !errors.Is(err, boom) {
		t.Fatalf("first turn: err = %v, want the approver's error", err)
	}
	if err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatalf("second turn: err = %v, want none", err)
	}
}
