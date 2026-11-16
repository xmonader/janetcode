package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A tool the model loads with tool_search has to be in the very next
// request of the same turn. The turn's tool list used to be frozen at the
// turn's start, so a headless -p run (one turn) was told "callable from now
// on" for a tool whose schema it was never sent; live, models searched
// again and again until the repeat guard stopped them.
func TestToolSearchLoadReachesTheNextRequestOfTheTurn(t *testing.T) {
	reg := tool.NewRegistry()
	reg.AddDeferred(funcTool{name: "late_tool", run: func(json.RawMessage) (string, error) { return "ok", nil }})
	reg.Add(&tool.ToolSearch{Registry: reg})
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "1", Name: "tool_search", Input: json.RawMessage(`{"query":"late_tool"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a := &Agent{Provider: f, Tools: reg, Model: "m"}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	has := func(req llm.Request) bool {
		for _, d := range req.Tools {
			if d.Name == "late_tool" {
				return true
			}
		}
		return false
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	if has(f.requests[0]) {
		t.Fatal("deferred tool was sent before it was loaded")
	}
	if !has(f.requests[1]) {
		t.Fatal("the tool tool_search loaded is missing from the next request of the same turn")
	}
}
