package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/tool"
)

// The harness section is the only per-request cost of making janetcode
// aware of itself. These tests are what keeps it that way: they measure
// it with the estimator the agent itself uses for the context gauge,
// print the number so a change to the wording shows up in the log, and
// compare two real requests to a fake provider byte for byte to prove
// that nothing else about a turn moved.

func TestHarnessSectionFitsItsBudget(t *testing.T) {
	got := estimateTokens(prompt.Harness)
	t.Logf("harness prompt section: %d bytes, ~%d estimated tokens (budget %d)",
		len(prompt.Harness), got, prompt.HarnessBudget)
	if got > prompt.HarnessBudget {
		t.Fatalf("harness section is ~%d tokens, over the budget of %d", got, prompt.HarnessBudget)
	}
}

func TestHarnessSectionIsInTheCachedPrefix(t *testing.T) {
	s := prompt.System(t.TempDir())
	if !strings.Contains(s, prompt.Harness) {
		t.Fatal("the base prompt does not contain the harness section")
	}
	// openai.applyCacheBreakpoints splits on "# Environment"; anything
	// after it is re-sent uncached every request. The section is static,
	// so it belongs in front of the split.
	if strings.Index(s, prompt.Harness) > strings.Index(s, "\n# Environment\n") {
		t.Fatal("the harness section is below the cache breakpoint, so it is paid for uncached every request")
	}
}

// TestSystemPromptGrewByTheSectionAndNothingElse is the leanness
// snapshot: a whole turn is run twice against the fake provider, once
// with the section and once with it removed, and every byte of the
// request other than the section itself has to be identical.
func TestSystemPromptGrewByTheSectionAndNothingElse(t *testing.T) {
	run := func(system string) llm.Request {
		f := &fake{responses: []*llm.Response{text("done")}}
		a, _ := newAgent(t, f, nil)
		a.System = system
		if err := a.Run(context.Background(), "hello", nil); err != nil {
			t.Fatal(err)
		}
		if len(f.requests) != 1 {
			t.Fatalf("%d requests", len(f.requests))
		}
		return f.requests[0]
	}
	full := prompt.System(t.TempDir())
	without := strings.Replace(full, prompt.Harness, "", 1)
	if without == full {
		t.Fatal("the section is not in the prompt")
	}
	with, before := run(full), run(without)

	if d := estimateTokens(with.System) - estimateTokens(before.System); d > prompt.HarnessBudget {
		t.Fatalf("system prompt grew by ~%d tokens, over the budget of %d", d, prompt.HarnessBudget)
	} else {
		t.Logf("system prompt: ~%d → ~%d estimated tokens (+%d)",
			estimateTokens(before.System), estimateTokens(with.System), d)
	}
	if strings.Replace(with.System, prompt.Harness, "", 1) != before.System {
		t.Fatal("the system prompt changed by something other than the harness section")
	}
	if len(with.Tools) != len(before.Tools) {
		t.Fatalf("tool definitions changed: %d → %d", len(before.Tools), len(with.Tools))
	}
	toolBytes := func(r llm.Request) int {
		n := 0
		for _, d := range r.Tools {
			n += len(d.Name) + len(d.Description) + len(d.InputSchema)
		}
		return n
	}
	if toolBytes(with) != toolBytes(before) {
		t.Fatalf("tool bytes changed: %d → %d", toolBytes(before), toolBytes(with))
	}
	a, _ := json.Marshal(with.Messages)
	b, _ := json.Marshal(before.Messages)
	if string(a) != string(b) {
		t.Fatal("the messages of the turn changed")
	}
}

// TestIntrospectCostsNothingUntilItIsAskedFor is the other half of the
// budget: the tool that answers "what can janetcode do" must not be in
// the request until the model fetches it.
func TestIntrospectCostsNothingUntilItIsAskedFor(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok"), text("ok")}}
	a, _ := newAgent(t, f, nil)
	a.Tools.AddDeferred(feature.Introspect{Catalog: feature.New()})
	a.Tools.Add(&tool.ToolSearch{Registry: a.Tools})
	if err := a.Run(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.requests[0].Tools {
		if d.Name == "introspect" {
			t.Fatal("the deferred introspect tool was sent in the first request")
		}
	}
	if !a.Tools.Activate("introspect") {
		t.Fatal("introspect was not registered as deferred")
	}
	if err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.requests[1].Tools {
		if d.Name == "introspect" {
			found = true
		}
	}
	if !found {
		t.Fatal("introspect did not appear after being activated")
	}
}
