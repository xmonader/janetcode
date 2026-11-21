package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestCancelledCompletionGateDoesNotFinishTurn(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "end_turn", true: "empty_tool_use"}[malformed], func(t *testing.T) {
			last := text("done")
			if malformed {
				last.StopReason = llm.StopToolUse
			}
			f := &fake{responses: []*llm.Response{
				toolCall("w", "write_file", `{"path":"a.txt","content":"changed"}`), last,
			}}
			a, root := newAgent(t, f, nil)
			a.AutoApprove, a.MaxIterations = true, 2
			a.Off = map[string]bool{"verify_baseline": true}
			a.Log = &session.Memory{}
			a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"true"}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done, cancelledGate := false, false
			err := a.Run(ctx, "go", func(e Event) {
				if e.Kind == EvNotice && strings.HasPrefix(e.Text, "verifying:") {
					cancelledGate = true
					cancel()
				}
				done = done || e.Kind == EvTurnDone
			})
			if !cancelledGate || !errors.Is(err, context.Canceled) || done {
				t.Fatalf("gate cancelled=%v, err=%v, completion event=%v", cancelledGate, err, done)
			}
			for _, e := range a.Log.Events() {
				if e.Type == session.TypeTurnEnd && e.Reason != "cancelled" {
					t.Fatalf("cancelled gate logged a successful turn end: %+v", e)
				}
			}
		})
	}
}
