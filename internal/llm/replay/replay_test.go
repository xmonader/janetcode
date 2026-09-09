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
