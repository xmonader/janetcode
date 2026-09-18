package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The minute you spend deciding whether to allow a write is not the
// write taking a minute. A report that charges the prompt to the tool
// says bash is slow when what was slow is the answer.
func TestApprovalTimeIsChargedToTheUserNotTheTool(t *testing.T) {
	const think = 40 * time.Millisecond
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"b.txt","content":"x"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		time.Sleep(think)
		return Allow, nil
	}))
	a.Log = &session.Memory{}
	if err := a.Run(context.Background(), "write it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	st := a.Stats()
	if st.WaitCount != 1 {
		t.Fatalf("the prompt was not counted as waiting on the user: %d", st.WaitCount)
	}
	if st.WaitTime < think {
		t.Fatalf("waited %s, want at least %s", st.WaitTime, think)
	}
	var tool ToolStat
	for _, s := range st.Tools {
		if s.Name == "write_file" {
			tool = s
		}
	}
	if tool.Calls != 1 {
		t.Fatalf("write_file not recorded: %+v", st.Tools)
	}
	if tool.Time >= think {
		t.Fatalf("the tool was charged the prompt: %s of a %s wait", tool.Time, think)
	}
	if st.TurnTime < think {
		t.Fatalf("the turn's wall time (%s) does not even cover the prompt", st.TurnTime)
	}
}

// The slices have to add up: whatever is not the provider, a tool or the
// user is the harness, and it has to be visible rather than lost.
func TestPerfSlicesAccountForTheWholeTurn(t *testing.T) {
	s := Stats{
		Turns: 1, ModelCalls: 2, ModelTime: 6 * time.Second, TurnTime: 10 * time.Second,
		WaitTime: 1 * time.Second, WaitCount: 1,
		Models: []ModelStat{{Name: "m", Calls: 2, Time: 6 * time.Second, Slowest: 5 * time.Second}},
		Tools:  []ToolStat{{Name: "bash", Calls: 3, Time: 2 * time.Second, Slowest: 1500 * time.Millisecond}},
	}
	var sum time.Duration
	byName := map[string]PerfSlice{}
	for _, p := range s.Perf() {
		sum += p.Time
		byName[p.Name] = p
	}
	if sum != s.TurnTime {
		t.Fatalf("slices sum to %s, turn was %s: %+v", sum, s.TurnTime, s.Perf())
	}
	if byName["harness"].Time != time.Second {
		t.Fatalf("harness remainder = %s, want 1s", byName["harness"].Time)
	}
	if byName["model"].Slowest != 5*time.Second || byName["tools"].Slowest != 1500*time.Millisecond {
		t.Fatalf("the slowest single call is lost: %+v", byName)
	}
	out := FormatPerf(s, 30*time.Second)
	for _, want := range []string{"model", "tools", "you", "harness", "bash", "slowest", "by model"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the report leaves out %q:\n%s", want, out)
		}
	}
	// 6s of a 10s turn is 60%, and the turn is a third of the session.
	if !strings.Contains(out, "60%") || !strings.Contains(out, "33%") {
		t.Fatalf("the shares are wrong:\n%s", out)
	}
}

// Parallel tool calls can add up to more than the turn's wall clock;
// the report says so rather than printing a negative remainder.
func TestPerfSaysWhenToolsOverlapped(t *testing.T) {
	s := Stats{Turns: 1, TurnTime: time.Second, Tools: []ToolStat{{Name: "bash", Calls: 4, Time: 3 * time.Second}}}
	got := s.Perf()
	last := got[len(got)-1]
	if last.Name != "harness" || last.Time != 0 || !strings.Contains(last.Note, "parallel") {
		t.Fatalf("overlap not reported: %+v", last)
	}
}

func TestPerfSaysNothingRanYet(t *testing.T) {
	if out := FormatPerf(Stats{}, time.Minute); !strings.Contains(out, "no turn") {
		t.Fatalf("empty report = %q", out)
	}
}

// A provider that takes a minute to start and one that takes a minute
// to finish are different problems with the same total. The split has
// to reach the report.
func TestPerfSeparatesWaitingToStartFromGenerating(t *testing.T) {
	s := Stats{
		Turns: 1, ModelCalls: 2, ModelTime: 10 * time.Second, ModelTTFT: 7 * time.Second,
		TurnTime: 12 * time.Second,
		Models:   []ModelStat{{Name: "slowpoke", Calls: 2, Time: 10 * time.Second, TTFT: 7 * time.Second}},
	}
	out := FormatPerf(s, time.Minute)
	if !strings.Contains(out, "before the first token") || !strings.Contains(out, "70%") {
		t.Fatalf("the first-token share is missing:\n%s", out)
	}
	if !strings.Contains(out, "waiting to start") {
		t.Fatalf("the per-model split is missing:\n%s", out)
	}
}

// slowStart is a provider that sits silent and then streams: the shape
// that makes a total duration useless, because the whole of it was the
// queue and none of it was the answer.
type slowStart struct {
	quiet time.Duration
	reply string
}

func (slowStart) Name() string { return "slowstart" }

func (s slowStart) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	select {
	case <-time.After(s.quiet):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if emit != nil {
		// A status event is the harness talking, not the model: it must
		// not be mistaken for the first token.
		emit(llm.Event{Type: llm.EventStatus, Text: "connected"})
		emit(llm.Event{Type: llm.EventTextDelta, Text: s.reply})
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, s.reply),
		StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, OutputTokens: 4}}, nil
}

// The session log has always had a field for the time-to-first-token
// split and nothing ever wrote it, so a session read back had a total
// and no way to tell a queued call from a long answer.
func TestTheFirstTokenSplitIsMeasuredAndLogged(t *testing.T) {
	const quiet = 60 * time.Millisecond
	mem := &session.Memory{}
	a := &Agent{Provider: slowStart{quiet: quiet, reply: "hello"}, Tools: tool.NewRegistry(), Model: "m", Log: mem}
	if err := a.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	var got session.Event
	for _, e := range mem.Events() {
		if e.Type == session.TypeAssistant {
			got = e
		}
	}
	if got.TTFTMs < quiet.Milliseconds() {
		t.Fatalf("ttft = %dms, want at least %dms of silence", got.TTFTMs, quiet.Milliseconds())
	}
	if got.DecodeMs < 0 || got.TTFTMs+got.DecodeMs > 5000 {
		t.Fatalf("the split is not a split: ttft=%d decode=%d", got.TTFTMs, got.DecodeMs)
	}
	if st := a.Stats(); st.ModelTTFT < quiet {
		t.Fatalf("stats did not record the wait: %s", st.ModelTTFT)
	}
}

// A status event is the harness, not the model. Counting it as the
// first token would report every queued call as instant.
func TestAStatusEventIsNotTheFirstToken(t *testing.T) {
	if isTokenDelta(llm.Event{Type: llm.EventStatus, Text: "connected"}) {
		t.Fatal("a status event counted as a token")
	}
	if isTokenDelta(llm.Event{Type: llm.EventTextDelta, Text: ""}) {
		t.Fatal("an empty delta counted as a token")
	}
	if !isTokenDelta(llm.Event{Type: llm.EventToolUseStart}) {
		t.Fatal("a tool call is output too")
	}
}
