package agent

import (
	"context"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestPrepareGetsTurnContext: turn preparation attaches the repo map and
// recalls memory, which walks the tree, asks git what changed and can go
// out to an embedding provider. It used to do all of that on a
// context.Background() of its own, so ^C during the pause before a turn's
// first model call left it running. It now gets the turn's own context.
func TestPrepareGetsTurnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &fake{responses: []*llm.Response{text("done")}}
	a := &Agent{Provider: prov, Model: "m"}

	entered := make(chan struct{})
	var sawErr error
	a.Prepare = func(pctx context.Context, blocks []llm.Block) ([]llm.Block, string) {
		close(entered)
		select {
		case <-pctx.Done():
			sawErr = pctx.Err()
		case <-time.After(5 * time.Second):
		}
		return blocks, ""
	}

	go func() {
		<-entered
		cancel()
	}()
	err := a.Run(ctx, "hello", func(Event) {})
	if sawErr == nil {
		t.Fatal("Prepare was given a context the turn's cancellation cannot reach")
	}
	if err == nil {
		t.Fatal("a turn cancelled during preparation should not report success")
	}
}
