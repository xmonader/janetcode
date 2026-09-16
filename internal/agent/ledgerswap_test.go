package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

// TestLedgerSwapDuringTurn: /cd swaps the permission ledger from a
// front-end goroutine (SetLedger, under settingsMu) while the turn
// goroutine is recording approvals into it. The turn used to read the
// field directly, which is a data race; it goes through PermLedger now.
// Run with -race, this fails on the old code.
func TestLedgerSwapDuringTurn(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"1"}`),
		toolCall("t2", "write_file", `{"path":"y","content":"2"}`),
		text("done"),
	}}
	approver := ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil })
	a, _ := newAgent(t, f, approver)
	a.SetLedger(perm.OpenLedger(t.TempDir(), "one"))
	other := perm.OpenLedger(t.TempDir(), "two")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				a.SetLedger(other)
			}
		}
	}()
	err := a.Run(context.Background(), "write", nil)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if a.PermLedger() == nil {
		t.Fatal("the ledger should still be readable after the turn")
	}
}
