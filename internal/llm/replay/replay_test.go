package replay

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A session log as the harness writes it: envelope fields and event fields
// on the same object.
const recording = `{"type":"meta","meta":{"id":"s1"}}
{"type":"event","seq":1,"t":"turn/start","turn":1}
{"type":"event","seq":2,"t":"user/message","turn":1,"surface":true,"message":{"role":"user","blocks":[{"type":"text","text":"count the go files"}]}}
{"type":"event","seq":3,"t":"assistant/message","turn":1,"surface":true,"message":{"role":"assistant","blocks":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"c1","name":"bash","input":{"command":"ls"}}]},"usage":{"input_tokens":10,"output_tokens":5}}
{"type":"event","seq":4,"t":"tool/result","turn":1,"surface":true,"message":{"role":"user","blocks":[{"type":"tool_result","tool_use_id":"c1","text":"a.go"}]}}
{"type":"event","seq":5,"t":"assistant/message","turn":1,"surface":true,"message":{"role":"assistant","blocks":[{"type":"text","text":"One file."}]},"usage":{"input_tokens":20,"output_tokens":3}}
{"type":"event","seq":6,"t":"turn/end","turn":1,"reason":"end_turn"}
`

func TestReadRecoversPromptsAndResponses(t *testing.T) {
	p, err := Read(strings.NewReader(recording))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Prompts(); len(got) != 1 || got[0] != "count the go files" {
		t.Fatalf("prompts = %v", got)
	}
	if p.Remaining() != 2 {
		t.Fatalf("remaining = %d, want 2", p.Remaining())
	}

	// The first response asked for a tool, so the loop must be told to run
	// one rather than to stop.
	r1, err := p.Stream(context.Background(), llm.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.StopReason != llm.StopToolUse {
		t.Fatalf("first stop reason = %q", r1.StopReason)
	}
	uses := r1.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "bash" {
		t.Fatalf("tool uses = %+v", uses)
	}
	if r1.Usage.InputTokens != 10 {
		t.Fatalf("usage was lost: %+v", r1.Usage)
	}

	// The second ended the turn.
	r2, err := p.Stream(context.Background(), llm.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StopReason != llm.StopEndTurn || r2.Message.Text() != "One file." {
		t.Fatalf("second response = %+v", r2)
	}

	// Asking for more model calls than were recorded is a behaviour
	// change, and must fail rather than be papered over.
	if _, err := p.Stream(context.Background(), llm.Request{}, nil); err == nil {
		t.Fatal("running past the end of the recording should fail")
	}
}

func TestStreamEmitsBlocks(t *testing.T) {
	p, _ := Read(strings.NewReader(recording))
	var kinds []llm.EventType
	if _, err := p.Stream(context.Background(), llm.Request{}, func(e llm.Event) { kinds = append(kinds, e.Type) }); err != nil {
		t.Fatal(err)
	}
	want := []llm.EventType{llm.EventTextDelta, llm.EventToolUseStart, llm.EventToolUseEnd}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events = %v, want %v", kinds, want)
		}
	}
}

func TestReadRejectsAnEmptyRecording(t *testing.T) {
	if _, err := Read(strings.NewReader(`{"type":"meta","meta":{"id":"s1"}}`)); err == nil {
		t.Fatal("a recording with no assistant messages is not replayable")
	}
}

// TestReplayCatchesADroppedToolCall is the point of the whole package: a
// change that alters which tools a response produces shows up as a
// different call sequence. This is the shape of every streaming bug fixed
// in this project — two calls becoming one — and it is caught here with no
// API key and no cost.
func TestReplayCatchesADroppedToolCall(t *testing.T) {
	two := `{"type":"event","t":"user/message","message":{"role":"user","blocks":[{"type":"text","text":"go"}]}}
{"type":"event","t":"assistant/message","message":{"role":"assistant","blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{"path":"x"}},{"type":"tool_use","id":"b","name":"grep","input":{"pattern":"y"}}]}}
{"type":"event","t":"assistant/message","message":{"role":"assistant","blocks":[{"type":"text","text":"done"}]}}
`
	// The same session as it would have been recorded by a harness that
	// merged the two calls into one — the bug this project shipped.
	merged := `{"type":"event","t":"user/message","message":{"role":"user","blocks":[{"type":"text","text":"go"}]}}
{"type":"event","t":"assistant/message","message":{"role":"assistant","blocks":[{"type":"tool_use","id":"a","name":"read_filegrep","input":{"path":"x"}}]}}
{"type":"event","t":"assistant/message","message":{"role":"assistant","blocks":[{"type":"text","text":"done"}]}}
`
	names := func(src string) []string {
		p, err := Read(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Stream(context.Background(), llm.Request{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, u := range r.Message.ToolUses() {
			out = append(out, u.Name)
		}
		return out
	}
	good, bad := names(two), names(merged)
	if len(good) != 2 || good[0] != "read_file" || good[1] != "grep" {
		t.Fatalf("healthy recording replays as %v", good)
	}
	if len(bad) == len(good) {
		t.Fatal("the merged recording should not replay as two calls")
	}
	if bad[0] != "read_filegrep" {
		t.Fatalf("bad recording replays as %v", bad)
	}
}

// fakeLive is a stub live provider for testing Until's boundary: it never
// touches the recording and reports how many times it was asked.
type fakeLive struct{ calls int }

func (f *fakeLive) Name() string { return "fake-live" }

func (f *fakeLive) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	f.calls++
	m := llm.TextMessage(llm.RoleAssistant, "live reply")
	return &llm.Response{Message: m, StopReason: llm.StopEndTurn}, nil
}

// twoTurns is a recording with two model calls in turn 1 and one in turn
// 2, the shape Until's boundary logic has to get right: the switch must
// land between turn 1's second call and turn 2's first, not after some
// fixed count of Stream calls.
const twoTurns = `{"type":"event","seq":1,"t":"user/message","turn":1,"message":{"role":"user","blocks":[{"type":"text","text":"first"}]}}
{"type":"event","seq":2,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"tool_use","id":"a","name":"bash","input":{"command":"ls"}}]}}
{"type":"event","seq":3,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"text","text":"done with turn 1"}]}}
{"type":"event","seq":4,"t":"user/message","turn":2,"message":{"role":"user","blocks":[{"type":"text","text":"second"}]}}
{"type":"event","seq":5,"t":"assistant/message","turn":2,"message":{"role":"assistant","blocks":[{"type":"text","text":"recorded turn 2 reply, should never be seen"}]}}
`

// TestUntilServesRecordedThenSwitches is the point of Until: turn 1's two
// model calls come from the recording (no live call at all), and turn 2's
// call is answered by the live provider instead of the recording's own
// (very different) turn-2 reply.
func TestUntilServesRecordedThenSwitches(t *testing.T) {
	rp, err := Read(strings.NewReader(twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	live := &fakeLive{}
	p := Until(rp, 2, live)

	r1, err := p.Stream(context.Background(), llm.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Message.ToolUses()) != 1 || r1.Message.ToolUses()[0].Name != "bash" {
		t.Fatalf("turn 1 call 1 = %+v, want the recorded bash call", r1)
	}
	if live.calls != 0 {
		t.Fatalf("live called during turn 1: %d", live.calls)
	}

	r2, err := p.Stream(context.Background(), llm.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Message.Text() != "done with turn 1" {
		t.Fatalf("turn 1 call 2 = %+v, want the recorded end-of-turn reply", r2)
	}
	if live.calls != 0 {
		t.Fatalf("live called before turn 2: %d", live.calls)
	}

	r3, err := p.Stream(context.Background(), llm.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if live.calls != 1 {
		t.Fatalf("live.calls = %d, want 1 after turn 2's first call", live.calls)
	}
	if r3.Message.Text() != "live reply" {
		t.Fatalf("turn 2 = %+v, want the live reply, not the recording's", r3)
	}

	// Once switched, later calls stay live even though the recording is
	// exhausted (this recording has only one turn-2 response).
	if _, err := p.Stream(context.Background(), llm.Request{}, nil); err != nil {
		t.Fatalf("a further call after switching should reach live, not fail on the exhausted recording: %v", err)
	}
	if live.calls != 2 {
		t.Fatalf("live.calls = %d, want 2", live.calls)
	}
}

// TestUntilFromTurnOneIsAllLive covers --from-turn 1: nothing is replayed
// and the very first call goes live.
func TestUntilFromTurnOneIsAllLive(t *testing.T) {
	rp, err := Read(strings.NewReader(twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	live := &fakeLive{}
	p := Until(rp, 1, live)
	if _, err := p.Stream(context.Background(), llm.Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if live.calls != 1 {
		t.Fatalf("live.calls = %d, want 1", live.calls)
	}
}

// TestUntilBeyondRecordingStaysRecorded covers a from-turn beyond every
// recorded turn: every recorded call is served and live is never reached.
func TestUntilBeyondRecordingStaysRecorded(t *testing.T) {
	rp, err := Read(strings.NewReader(twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	live := &fakeLive{}
	p := Until(rp, 99, live)
	for i := 0; i < 3; i++ {
		if _, err := p.Stream(context.Background(), llm.Request{}, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if live.calls != 0 {
		t.Fatalf("live.calls = %d, want 0", live.calls)
	}
}
