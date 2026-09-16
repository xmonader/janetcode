package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestAdvisorIsASideQueryWithoutTools(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("1) read parse.go\n2) off-by-one\n3) go test ./...")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "exec-model", AdvisorModel: "strong-model", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "fix the parser"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "secret thoughts"},
			{Type: llm.BlockText, Text: "I keep failing the test"},
		}},
	}}
	adv := &AdvisorTool{Parent: a}
	out, err := adv.Run(context.Background(), []byte(`{"question":"the test still fails"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parse.go") {
		t.Fatalf("out = %q", out)
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	req := f.requests[0]
	if req.System != advisorSystem {
		t.Fatalf("system = %q", req.System)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("tools leaked: %d", len(req.Tools))
	}
	if req.Model != "strong-model" {
		t.Fatalf("model = %q", req.Model)
	}
	blob := ""
	for _, m := range req.Messages {
		blob += m.Text()
	}
	if strings.Contains(blob, "secret thoughts") {
		t.Fatal("thinking leaked")
	}
}

func TestAdvisorCapsUsesPerTurn(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("a"), text("b"), text("c"), text("d")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	adv := &AdvisorTool{Parent: a}
	for i := 0; i < 3; i++ {
		if _, err := adv.Run(context.Background(), []byte(`{}`)); err != nil {
			t.Fatalf("use %d: %v", i+1, err)
		}
	}
	if _, err := adv.Run(context.Background(), []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "3 times") {
		t.Fatalf("4th use: %v", err)
	}
}
