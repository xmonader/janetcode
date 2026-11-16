package agent

import (
	"context"
	"errors"
	"github.com/xmonader/janetcode/internal/session"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/delegate"
	"github.com/xmonader/janetcode/internal/llm"
)

// A delegate call is put to the approver like a bash command: a denial
// stops the CLI from ever starting, an approval runs it, and the CLI's
// progress reaches the event stream as status lines.
func TestDelegateGoesThroughApprover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLIs are shell scripts")
	}
	bin, err := filepath.Abs("../delegate/testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	// Without the fakes the real, signed-in CLIs on PATH would run (and bill).
	for _, name := range []string{"claude", "codex"} {
		if fi, err := os.Stat(filepath.Join(bin, name)); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("fake %s missing or not executable in %s: %v", name, bin, err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("DELEGATE_ARGSOUT", started)

	run := func(d Decision) (*Agent, []Event, int) {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "delegate", `{"agent":"claude","prompt":"ok"}`),
			text("done"),
		}}
		asked := 0
		var names []string
		a, _ := newAgent(t, f, ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
			asked++
			names = append(names, c.Name)
			return d, nil
		}))
		a.Tools.Add(&delegate.Tool{Config: delegate.Config{Enabled: true}, Dir: func() string { return "" }})
		evs, emit := collect()
		if err := a.Run(context.Background(), "delegate it", emit); err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "delegate" {
			t.Fatalf("approver saw %v", names)
		}
		return a, *evs, asked
	}

	a, _, _ := run(Deny)
	if r := a.Messages[2].Blocks[0]; !r.IsError || !strings.Contains(r.Text, "denied") {
		t.Fatalf("deny result = %+v", r)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("a denied delegate still started the CLI")
	}

	a, evs, _ := run(Allow)
	if r := a.Messages[2].Blocks[0]; r.IsError || !strings.Contains(r.Text, "the answer is 42") {
		t.Fatalf("allow result = %+v", r)
	}
	var phases []string
	for _, e := range evs {
		if e.Kind == EvPhase {
			phases = append(phases, e.Text)
		}
	}
	if !strings.Contains(strings.Join(phases, "\n"), "claude: found it") {
		t.Fatalf("progress did not reach the event stream: %q", phases)
	}
}

func fakeCLIs(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLIs are shell scripts")
	}
	bin, err := filepath.Abs("../delegate/testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func delegateTool(a *Agent) *delegate.Tool {
	return &delegate.Tool{
		Config: delegate.Config{Enabled: true},
		Dir:    func() string { return "" },
		OnUsage: func(name string, r delegate.Result) {
			a.AddDelegateUsage(name, r.InputTokens, r.OutputTokens, r.CostUSD, r.HasCost)
		},
	}
}

// A delegate's price and tokens land in their own bucket: the cost reaches
// the session total, the tokens stay out of the model's own usage so its
// cache-hit figure is untouched, and a CLI with no price (codex) counts
// tokens but leaves the cost unknown rather than zero.
func TestDelegateUsageCountsSeparately(t *testing.T) {
	fakeCLIs(t)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "delegate", `{"agent":"claude","prompt":"ok"}`),
		toolCall("t2", "delegate", `{"agent":"codex","prompt":"ok"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil }))
	a.Tools.Add(delegateTool(a))
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := a.UsageSnapshot(); got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 || got.InputTokens != 3 || got.OutputTokens != 3 || got.CostUSD != 0 {
		t.Fatalf("delegate usage leaked into the model's usage: %+v", got)
	}
	d := a.UsageDelegated()
	if len(d) != 2 || d[0].Agent != "claude" || d[1].Agent != "codex" {
		t.Fatalf("delegated = %+v", d)
	}
	if c := d[0]; c.Runs != 1 || c.InputTokens != 115 || c.OutputTokens != 7 || !c.HasCost || c.CostUSD != 0.0123 {
		t.Fatalf("claude row = %+v", c)
	}
	if x := d[1]; x.InputTokens != 50 || x.OutputTokens != 9 || x.HasCost || x.CostUSD != 0 {
		t.Fatalf("codex row = %+v", x)
	}
	usd, _, _, known := a.SessionCost()
	if !known || usd != 0.0123 {
		t.Fatalf("session cost = %v known=%v, want exactly claude's 0.0123", usd, known)
	}
	out := FormatUsageDelegated(d)
	if !strings.Contains(out, "$0.012") || !strings.Contains(out, "cost unknown") {
		t.Fatalf("format = %q", out)
	}
	if !strings.Contains(a.CostNote(), "includes delegated $0.012") {
		t.Fatalf("cost note = %q", a.CostNote())
	}
	// The log carries it, so a resume restores the same buckets.
	var ev []session.Event
	for _, row := range d {
		u := llm.Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CostUSD: row.CostUSD}
		ev = append(ev, session.Event{Type: session.TypeUsage, Model: "delegate:" + row.Agent, Usage: &u})
	}
	b, _ := newAgent(t, &fake{}, nil)
	b.restoreUsage(ev)
	if got := b.UsageDelegated(); len(got) != 2 || got[0] != d[0] || got[1] != d[1] {
		t.Fatalf("restored = %+v, want %+v", got, d)
	}
	if b.UsageSnapshot() != (llm.Usage{}) {
		t.Fatalf("restore put delegate tokens in the model's usage: %+v", b.UsageSnapshot())
	}
}

// A delegate that spends past --max-cost ends the run like any other
// overrun, even though no model call was priced.
func TestDelegateCostStopsOnBudget(t *testing.T) {
	fakeCLIs(t)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "delegate", `{"agent":"claude","prompt":"ok"}`),
		text("never reached"),
	}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil }))
	a.Tools.Add(delegateTool(a))
	a.SetMaxCost(0.01)
	err := a.Run(context.Background(), "go", func(Event) {})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}
