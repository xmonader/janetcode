package agent

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The session, durable log and shared account must agree on the spend of
// calls whose usage frame never arrived. Recounting a synthetic reply must
// not bill the shared account a second time.
func assertStreamSpend(t *testing.T, a *Agent, calls int) {
	t.Helper()
	u := a.UsageSnapshot()
	if u.InputTokens == 0 || u.OutputTokens <= 1 {
		t.Fatalf("streamed usage was lost: %+v", u)
	}
	rows := a.UsageByModel()
	if len(rows) != 1 || rows[0].Calls != calls {
		t.Fatalf("usage counted other than once per call: %+v", rows)
	}
	want, _, _, known := a.SessionCost()
	b := a.sharedAccount()
	if !known || want <= 0 {
		t.Fatalf("unpriced streamed usage: cost=%g known=%v", want, known)
	}
	if b != nil {
		b.mu.Lock()
		spent := b.spent
		b.mu.Unlock()
		if math.Abs(want-spent) > 1e-12 {
			t.Fatalf("session cost=%g, shared account=%g", want, spent)
		}
	}
	restored := &Agent{Model: "m", Log: a.Log}
	restored.RestoreFromLog()
	if got := restored.UsageSnapshot(); got != u {
		t.Fatalf("restored usage=%+v, want %+v", got, u)
	}
}

func TestHarnessCutsPreserveStreamedUsage(t *testing.T) {
	for _, kind := range []string{"plan", "burst", "plan_no_budget", "burst_no_budget"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := newAgent(t, &fake{}, nil)
			a.Log = &session.Memory{}
			a.Price = &llm.Price{Input: 1, Output: 2}
			if !strings.HasSuffix(kind, "_no_budget") {
				a.SetMaxCost(1)
			}
			if strings.HasPrefix(kind, "plan") {
				a.Provider = &thinker{fake: fake{responses: []*llm.Response{nil, text("done")}}, reasoning: []int{2_000, 0}}
				a.PlanBudget = 1_000
			} else {
				a.Provider = &burster{fake: fake{responses: []*llm.Response{nil, text("done")}}, n: 400,
					path: func(int) string { return "a.txt" }}
			}
			if err := a.Run(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			assertStreamSpend(t, a, 2)
			if strings.HasPrefix(kind, "plan") && a.UsageSnapshot().ReasoningTokens == 0 {
				t.Fatal("cut reasoning was not counted")
			}
		})
	}
}

type usageFailureProvider struct {
	response *llm.Response
	err      error
	cancel   context.CancelFunc
	produced bool
}

func (*usageFailureProvider) Name() string { return "usage-failure" }
func (p *usageFailureProvider) Stream(_ context.Context, _ llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if p.produced {
		emit(llm.Event{Type: llm.EventTextDelta, Text: strings.Repeat("generated text ", 100)})
	}
	if p.cancel != nil {
		p.cancel()
	}
	return p.response, p.err
}

func TestFailedStreamsPreserveUsageOnce(t *testing.T) {
	for _, kind := range []string{"provider_error", "empty_partial_response", "reported_usage", "cancelled_reported_usage", "before_generation"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &usageFailureProvider{err: errors.New("stream disconnected"), produced: true}
			reported := llm.Usage{InputTokens: 200, OutputTokens: 30, CostUSD: .125}
			switch kind {
			case "empty_partial_response":
				p.response = &llm.Response{}
			case "reported_usage", "cancelled_reported_usage":
				p.response = &llm.Response{Usage: reported}
				if kind == "cancelled_reported_usage" {
					p.cancel, p.err = cancel, context.Canceled
				}
			case "before_generation":
				p.produced = false
			}
			a, _ := newAgent(t, &fake{}, nil)
			a.Log = &session.Memory{}
			a.Price = &llm.Price{Input: 1, Output: 2}
			a.SetMaxCost(1)
			if _, err := a.callOn(ctx, p, "m", callRoute{slot: "main"}, func(Event) {}); !errors.Is(err, p.err) {
				t.Fatalf("err=%v, want %v", err, p.err)
			}
			if kind == "before_generation" {
				if got := a.UsageSnapshot(); got != (llm.Usage{}) {
					t.Fatalf("invented usage before generation: %+v", got)
				}
				return
			}
			assertStreamSpend(t, a, 1)
			if p.response != nil && p.response.Usage != (llm.Usage{}) && a.UsageSnapshot() != reported {
				t.Fatalf("estimated over provider's reported usage: %+v", a.UsageSnapshot())
			}
		})
	}
}
