package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// burster streams its first reply as n read_file calls, one after another,
// and stops when the call is cancelled; later replies come from fake. Call
// i reads path(i).
type burster struct {
	fake
	n       int
	path    func(i int) string
	started int
}

func (f *burster) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if len(f.requests) > 0 {
		return f.fake.Stream(ctx, req, emit)
	}
	f.requests = append(f.requests, req)
	for i := range f.n {
		f.started++
		id := "c" + strings.Repeat("x", i)
		emit(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{Type: llm.BlockToolUse, ID: id, Name: "read_file"}})
		emit(llm.Event{Type: llm.EventToolUseDelta, Text: `{"path":`})
		emit(llm.Event{Type: llm.EventToolUseDelta, Text: `"` + f.path(i) + `"}`})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, context.Canceled
}

// A reply that keeps starting tool calls is ended at the one past the cap;
// the calls that arrived whole are run and the model is told the rest was
// dropped.
func TestBurstCapRunsTheFirstCalls(t *testing.T) {
	f := &burster{fake: fake{responses: []*llm.Response{nil, text("done")}}, n: 400, path: func(i int) string {
		if i == 0 {
			return "a.txt"
		}
		return fmt.Sprintf("f%d.txt", i)
	}}
	a, _ := newAgent(t, &f.fake, nil)
	a.Provider = f
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if f.started != maxBurstCalls+1 {
		t.Fatalf("the stream went on to call %d, want it ended at %d", f.started, maxBurstCalls+1)
	}
	if len(f.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.requests))
	}
	last := f.requests[1].Messages[len(f.requests[1].Messages)-1]
	results := 0
	for _, b := range last.Blocks {
		if b.Type == llm.BlockToolResult {
			results++
		}
	}
	if results != maxBurstCalls {
		t.Fatalf("tool results sent back = %d, want %d", results, maxBurstCalls)
	}
	if got := last.Blocks[0].Text; !strings.Contains(got, "alpha") {
		t.Fatalf("first result = %q, want the file", got)
	}
	if got := last.Blocks[len(last.Blocks)-1].Text; !strings.Contains(got, "only its first 24 calls were run") {
		t.Fatalf("last result = %q, want the note", got)
	}
}

// Only calls whose arguments arrived whole are kept.
func TestBurstReplyDropsACutCall(t *testing.T) {
	w := &burstWatch{}
	w.note(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{ID: "1", Name: "bash"}})
	w.note(llm.Event{Type: llm.EventToolUseDelta, Text: `{"command":"ls"}`})
	w.note(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{ID: "2", Name: "bash"}})
	w.note(llm.Event{Type: llm.EventToolUseDelta, Text: `{"command":"l`})
	if uses := w.reply("m").Message.ToolUses(); len(uses) != 1 || uses[0].ID != "1" {
		t.Fatalf("kept %v, want call 1 alone", uses)
	}
	if r := (&burstWatch{}).reply("m"); r.StopReason != llm.StopMaxTokens {
		t.Fatalf("an empty cut reply stops with %s, want max tokens", r.StopReason)
	}
}

// A reply that writes the same group of calls over and over is ended at
// the first repeat, and each call of the group runs once.
func TestBurstEndsAtTheFirstRepeat(t *testing.T) {
	group := []string{"a.txt", "b.txt", "c.txt"}
	f := &burster{fake: fake{responses: []*llm.Response{nil, text("done")}}, n: 400, path: func(i int) string { return group[i%3] }}
	a, _ := newAgent(t, &f.fake, nil)
	a.Provider = f
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if f.started != 5 {
		t.Fatalf("the stream went on to call %d, want it ended when call 5 showed call 4 repeated call 1", f.started)
	}
	last := f.requests[1].Messages[len(f.requests[1].Messages)-1]
	if n := len(last.Blocks); n != 3 {
		t.Fatalf("tool results = %d, want the 3 distinct calls", n)
	}
	if got := last.Blocks[2].Text; !strings.Contains(got, "started repeating calls") {
		t.Fatalf("last result = %q, want the repeat note", got)
	}
}
