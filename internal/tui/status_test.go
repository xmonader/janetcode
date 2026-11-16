package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
)

// /status prints the evidence model for the session, and the verdict line
// of a turn that changed code is followed by the same model's line.
func TestStatusCommandAndVerdictShowEvidence(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"a.go","content":"package m\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote it"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("write a.go")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "evidence: not_run") {
		t.Fatalf("the verdict line should be followed by the evidence line:\n%s", v)
	}
	h.keys("/status")
	h.enter()
	v = plain(h.m.View())
	for _, want := range []string{"evidence: — not_run", "success claims blocked", "edited and no check ran"} {
		if !strings.Contains(v, want) {
			t.Fatalf("/status lacks %q:\n%s", want, v)
		}
	}
}

// The evidence entry is red for a failed check and a caution otherwise,
// and is only added when the event carries a report.
func TestEvidenceEntryFollowsTheVerdict(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvVerdict, Text: "✗ check failed: x", IsError: true})
	if n := countKind(tr, entryVerdict); n != 1 {
		t.Fatalf("no report, no evidence entry: %d verdict entries", n)
	}
	rep := &evidence.Report{Status: evidence.Failed, Required: true, Blocks: true, Why: "w",
		Checks: []evidence.Check{{Command: "go test", Status: evidence.Failed, Fresh: true}}}
	tr.apply(agent.Event{Kind: agent.EvVerdict, Text: "✗ check failed: x", IsError: true, Evidence: rep})
	if n := countKind(tr, entryVerdict); n != 3 {
		t.Fatalf("want verdict + evidence entries, have %d verdict entries", n)
	}
	if e := tr.last(entryVerdict); !strings.HasPrefix(e.text, "✗ evidence: failed") || !e.isError {
		t.Fatalf("evidence entry = %+v", e)
	}
}

func countKind(tr *transcript, k entryKind) int {
	n := 0
	for _, e := range tr.entries {
		if e.kind == k {
			n++
		}
	}
	return n
}
