package agent

import (
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestPrefixWhereAttribution(t *testing.T) {
	sys := "s"
	tools := []llm.ToolDef{{Name: "a"}, {Name: "b"}}
	res := llm.ToolResultMessage(llm.ToolResult("1", "out", false))
	base := []llm.Message{msg(llm.RoleUser, "q"), msg(llm.RoleAssistant, "r"), res}
	fresh := func() *prefixWatch {
		w := &prefixWatch{}
		w.note(1, sys, base, tools)
		return w
	}
	// A pure append is not divergence.
	w := fresh()
	w.note(1, sys, append(append([]llm.Message{}, base...), msg(llm.RoleAssistant, "more")), tools)
	if w.where != "" || len(w.drifts) != 0 {
		t.Fatalf("append reported as divergence: %q", w.where)
	}
	w = fresh()
	w.note(2, "other", base, tools)
	if w.where != "system prompt changed" {
		t.Fatalf("system: %q", w.where)
	}
	w = fresh()
	w.note(2, sys, base, []llm.ToolDef{{Name: "b"}, {Name: "a"}})
	if w.where != "tool definitions changed" {
		t.Fatalf("tools reorder: %q", w.where)
	}
	w = fresh()
	edited := append([]llm.Message{}, base...)
	edited[2] = llm.ToolResultMessage(llm.ToolResult("1", "different", false))
	w.note(2, sys, edited, tools)
	if w.where != "message 3 (tool result) changed" {
		t.Fatalf("message: %q", w.where)
	}
	// The next, clean request clears it.
	w.note(2, sys, edited, tools)
	if w.where != "" {
		t.Fatalf("stale where: %q", w.where)
	}
}

func TestLedgerUsesDivergence(t *testing.T) {
	t0 := time.Now()
	var l cacheLedger
	l.record(llm.Usage{InputTokens: 100, CacheReadTokens: 9000}, "m", 1, t0, false, "")
	l.record(llm.Usage{InputTokens: 9100}, "m", 2, t0.Add(time.Second), false, "system prompt changed")
	if len(l.misses) != 1 || l.misses[0].Cause != "system prompt changed" {
		t.Fatalf("misses = %+v", l.misses)
	}
}
