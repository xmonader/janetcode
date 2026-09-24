package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// costly is a scripted response that bills a known amount.
func costly(usd float64, id, name, input string) *llm.Response {
	r := toolCall(id, name, input)
	r.Usage = llm.Usage{InputTokens: 1, OutputTokens: 1, CostUSD: usd}
	return r
}

func TestBudgetStopsWithinATurn(t *testing.T) {
	// Three calls of $0.40 against a $1 ceiling: the third crosses it, so
	// the turn must end there rather than run to its fourth.
	f := &fake{responses: []*llm.Response{
		costly(0.40, "1", "read_file", `{"path":"a.txt"}`),
		costly(0.40, "2", "read_file", `{"path":"a.txt"}`),
		costly(0.40, "3", "read_file", `{"path":"a.txt"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.MaxCostUSD = 1
	evs, emit := collect()
	err := a.Run(context.Background(), "read it a few times", emit)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("made %d model calls, want 3: the ceiling is checked after every call", len(f.requests))
	}
	if !strings.Contains(err.Error(), "$1.20") || !strings.Contains(err.Error(), "$1.00") {
		t.Fatalf("err = %v, want it to say what was spent of what", err)
	}
	// The user hears about it once: the returned error carries the
	// figures, a verdict marks the turn. A notice repeating the error made
	// one stop read four times over in a headless run.
	var notices, verdicts int
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "cost budget") {
			notices++
		}
		if e.Kind == EvVerdict && e.IsError && e.Text == "stopped on the cost budget" {
			verdicts++
		}
	}
	if notices != 0 || verdicts != 1 {
		t.Fatalf("notices = %d, verdicts = %d; want 0 and 1", notices, verdicts)
	}
	// The third call's tool was answered, not run, and the history still
	// pairs every tool_use with a result — otherwise the next request
	// would be refused by the provider.
	last := a.Messages[len(a.Messages)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("history ends with %s, want the tool results", last.Role)
	}
	if len(last.Blocks) == 0 || !strings.Contains(last.Blocks[0].Text, "was not run") {
		t.Fatalf("last message = %+v", last.Blocks)
	}
	assertPaired(t, a.Messages)
}

func TestBudgetIgnoredWhenNothingPricesTheRun(t *testing.T) {
	// No reported cost and no price table: the spend is unknown, and a
	// run must not be stopped on a figure of zero.
	f := &fake{responses: []*llm.Response{toolCall("1", "read_file", `{"path":"a.txt"}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.MaxCostUSD = 0.0001
	_, emit := collect()
	if err := a.Run(context.Background(), "read it", emit); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetUsesTheListPriceWhenTheProviderIsSilent(t *testing.T) {
	// A provider that reports no cost is still measurable when the model
	// has a price: 2M output tokens at $15/M is $30.
	f := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "thinking"), StopReason: llm.StopToolUse, Usage: llm.Usage{OutputTokens: 2_000_000}},
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.Price = &llm.Price{Input: 3, Output: 15}
	a.MaxCostUSD = 10
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("made %d model calls, want 1", len(f.requests))
	}
}

// assertPaired checks that every tool_use has a tool_result answering it.
func assertPaired(t *testing.T, msgs []llm.Message) {
	t.Helper()
	want := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockToolUse:
				want[b.ID] = true
			case llm.BlockToolResult:
				delete(want, b.ToolUseID)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("unanswered tool calls: %v", want)
	}
}

// A stream the watchdog ends is counted too: it is the harness saving a
// turn that would otherwise have waited forever.
func TestWatchdogKillsAreCounted(t *testing.T) {
	f := &fake{errs: []error{fmt.Errorf("call: %w", llm.ErrStreamWatchdog)}}
	a, _ := newAgent(t, f, nil)
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err == nil {
		t.Fatal("want the watchdog's error")
	}
	if st := a.Stats(); st.WatchdogKills != 1 || st.Saves() != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// A sub-cent ceiling is the normal case on a cheap model, and the compact
// money format rounds both figures to $0.0000 — a message that names
// neither the spend nor the ceiling it crossed.
func TestBudgetErrorNamesSubCentFigures(t *testing.T) {
	a := &Agent{}
	a.MaxCostUSD = 0.00005
	msg := a.budgetErr(0.00023).Error()
	if !strings.Contains(msg, "$0.00023") || !strings.Contains(msg, "$0.00005") {
		t.Fatalf("the budget error rounded its own figures away: %s", msg)
	}
}
