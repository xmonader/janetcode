package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A cost ceiling existed only as a headless flag, so an interactive
// session — the one that runs long enough to want one — could not set it.
func TestBudgetCommandSetsTheSessionCeiling(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	if c, ok := lookup("/budget"); !ok {
		t.Fatal("/budget is not a command")
	} else if c.avail != availIdle {
		t.Fatal("/budget writes a field the running turn reads, so it must wait for the turn")
	}
	h.m.opts.Agent.Price = &llm.Price{Input: 3, Output: 15}

	h.keys("/budget 0.50")
	h.enter()
	if h.m.opts.Agent.MaxCostUSD != 0.50 {
		t.Fatalf("ceiling = %v", h.m.opts.Agent.MaxCostUSD)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "$0.50") {
		t.Fatalf("the ceiling was not reported:\n%s", v)
	}

	// Alone it reports; the status of a session with a ceiling names it.
	h.keys("/budget")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "cost budget") {
		t.Fatalf("/budget alone said nothing:\n%s", v)
	}
	if s := h.m.budgetStatus(); !strings.Contains(s, "of $0.50") {
		t.Fatalf("budget status = %q", s)
	}

	h.keys("/budget off")
	h.enter()
	if h.m.opts.Agent.MaxCostUSD != 0 {
		t.Fatalf("ceiling after off = %v", h.m.opts.Agent.MaxCostUSD)
	}

	h.keys("/budget soon")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "is not an amount") {
		t.Fatalf("a word was accepted as money:\n%s", v)
	}
	if h.m.opts.Agent.MaxCostUSD != 0 {
		t.Fatalf("ceiling after nonsense = %v", h.m.opts.Agent.MaxCostUSD)
	}
}
