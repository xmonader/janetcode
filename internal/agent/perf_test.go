package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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
