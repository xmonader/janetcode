package agent

import (
	"context"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The compaction summary replays the head verbatim so a provider with
// prefix caching serves it from cache. That only works while the rest of
// the request is the one the turn itself sends: a different tool array
// moves the prefix and the whole replayed head is billed as fresh input.
func TestSummaryCallSendsTheTurnsOwnToolsInReadOnlyMode(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		text("nothing to change here"),
		text("handoff note"),
	}}
	a, _ := newAgent(t, f, nil)
	a.ReadOnly = true
	if err := a.Run(context.Background(), "have a look", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want the turn and the summary", len(f.requests))
	}
	turn, summary := f.requests[0], f.requests[1]
	if len(turn.Tools) == 0 {
		t.Fatal("the turn sent no tools; the comparison would be vacuous")
	}
	if hashTools(summary.Tools) != hashTools(turn.Tools) {
		t.Fatalf("the summary call sent %d tool definitions, the turn sent %d: the cached prefix does not match",
			len(summary.Tools), len(turn.Tools))
	}
	if summary.System != turn.System {
		t.Fatal("the summary call sent a different system prompt from the turn")
	}
	// Read-only hides the mutating tools, so a summary sending the whole
	// registry is exactly the regression this guards.
	for _, d := range summary.Tools {
		if d.Name == "write_file" {
			t.Fatal("the summary call offered write_file, which the read-only turn had hidden")
		}
	}
}
