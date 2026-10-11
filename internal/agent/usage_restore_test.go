package agent

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// A resumed session started its totals at zero, and spend no assistant
// message carries (side calls, subagents, a cancelled call's estimate)
// was never logged at all. Both are restored from the log now, once each.
func TestUsageSurvivesResume(t *testing.T) {
	log := &session.Memory{}
	a := &Agent{Model: "m", Log: log}
	reply := llm.Usage{InputTokens: 100, OutputTokens: 10}
	a.countUsage("m", reply)
	a.logEvent(session.Event{Type: session.TypeAssistant, Usage: &reply, Model: "m"})
	a.addUsage("m", llm.Usage{InputTokens: 50, OutputTokens: 5})       // a side call
	a.addSideUsage("sub", llm.Usage{InputTokens: 20, OutputTokens: 2}) // a subagent
	a.addSideUsage("m", cancelledUsage(30, "partial answer text", "")) // a cancelled call
	want := a.Usage

	b := &Agent{Model: "m", Log: log}
	b.RestoreFromLog()
	if b.Usage != want {
		t.Fatalf("restored %+v, want %+v", b.Usage, want)
	}
	if got := len(b.UsageByModel()); got != 2 {
		t.Fatalf("models restored = %d, want 2 (m and sub)", got)
	}
	b.RestoreFromLog() // restoring again must not double the totals
	if b.Usage != want {
		t.Fatalf("second restore %+v, want %+v", b.Usage, want)
	}
}
