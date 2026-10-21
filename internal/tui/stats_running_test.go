package tui

import (
	"fmt"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// /stats, /usage and /context may run while a turn does, and all three
// walked the history the turn goroutine is appending to: the first two
// for the size of the context (EstimateTokens), /context for its whole
// breakdown. The status line reads the estimate the agent published
// while a turn runs; these did not. Only -race sees it.
func TestInspectCommandsDuringATurnLeaveTheHistoryAlone(t *testing.T) {
	var rs []*llm.Response
	for i := 0; i < 40; i++ {
		// An unknown tool: answered with an error at once, no approval,
		// and two more messages in the history each round.
		rs = append(rs, &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: fmt.Sprintf("c%d", i), Name: "no_such_tool", Input: []byte(`{}`)},
		}}, StopReason: llm.StopToolUse})
	}
	rs = append(rs, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	h := newHarness(t, rs...)
	h.m.opts.Agent.MaxIterations = 100
	h.keys("go")
	h.enter()
	for i := 0; i < 200 && h.m.turn.running; i++ {
		h.m.statsCommand()
		h.m.cmdUsage("/usage", []string{"/usage"})
		h.m.contextCommand()
	}
	h.pump(func() bool { return !h.m.turn.running })
}
