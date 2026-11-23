package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/spend"
)

func spendGuard(t *testing.T, caps spend.Caps) *spend.Guard {
	t.Helper()
	path := filepath.Join(t.TempDir(), spend.File)
	return &spend.Guard{Ledger: spend.New(path, func() time.Time { return time.Date(2026, 11, 23, 10, 0, 0, 0, time.Local) }), Caps: caps}
}

// Every call is charged to the ledger, and the call after the cap is
// refused before it reaches the provider, with the --max-cost error so
// the headless runner exits 55.
func TestSpendCapRefusesTheNextCall(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		costly(0.60, "1", "read_file", `{"path":"a.txt"}`),
		costly(0.60, "2", "read_file", `{"path":"a.txt"}`),
		text("never reached"),
	}}
	a, _ := newAgent(t, f, nil)
	a.Spend = spendGuard(t, spend.Caps{Daily: 1})
	err := a.Run(context.Background(), "read it", func(Event) {})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	var ce *spend.CapError
	if !errors.As(err, &ce) || ce.Period != "daily" {
		t.Fatalf("err = %v, want the daily cap named", err)
	}
	if !strings.Contains(err.Error(), "$1.20") {
		t.Fatalf("err = %v, want the spend so far", err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("made %d provider calls, want 2", len(f.requests))
	}
	// A new run on a fresh agent is refused before any call.
	g := &fake{responses: []*llm.Response{text("x")}}
	b, _ := newAgent(t, g, nil)
	b.Spend = a.Spend
	if err := b.Run(context.Background(), "hi", func(Event) {}); !errors.Is(err, ErrBudget) || len(g.requests) != 0 {
		t.Fatalf("err = %v after %d calls, want a refusal before the call", err, len(g.requests))
	}
}

// No caps, no ledger: the default is byte-identical to the feature not
// existing.
func TestNoSpendGuardChargesNothing(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok")}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatal(err)
	}
}

// A ledger that cannot be written is logged and never blocks the call.
func TestSpendWriteFailureDoesNotBlock(t *testing.T) {
	f := &fake{responses: []*llm.Response{costly(0.1, "1", "read_file", `{"path":"a.txt"}`), text("done")}}
	a, root := newAgent(t, f, nil)
	// A directory where the ledger file should be makes the rename fail.
	bad := filepath.Join(root, spend.File)
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	a.Spend = &spend.Guard{Ledger: spend.New(bad, nil), Caps: spend.Caps{Daily: 5}}
	if err := a.Run(context.Background(), "read", func(Event) {}); err != nil {
		t.Fatalf("a failed ledger write blocked the run: %v", err)
	}
}
