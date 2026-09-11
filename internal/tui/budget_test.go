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

// A budget set before the first turn used to be met with "nothing prices
// this model, so the spend is unknown and the budget cannot stop
// anything" — for every model, priced or not, because nothing had been
// spent yet. The status line then went on reporting the provider's own
// figure against the ceiling, two lines apart.
func TestBudgetDoesNotCallAModelUnpricedBeforeItHasBoughtAnything(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Price = nil
	h.keys("/budget 0.50")
	h.enter()
	if v := plain(h.m.View()); strings.Contains(v, "nothing prices this model") {
		t.Fatalf("warned about an unknown spend before the first call:\n%s", v)
	}
	// Once there has been a call and there is still no figure, it says so.
	h.m.opts.Agent.Usage = llm.Usage{InputTokens: 1000, OutputTokens: 10}
	h.keys("/budget 0.60")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing prices this model") {
		t.Fatalf("a model nothing can price went unmentioned after a call:\n%s", v)
	}
}
