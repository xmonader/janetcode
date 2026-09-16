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
	if !strings.Contains(out, "parse.go") || !strings.Contains(out, "[advisor strong-model]") {
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

func TestAdvisorRoutesToItsOwnProvider(t *testing.T) {
	exec := &fake{name: "exec", responses: []*llm.Response{text("should not run")}}
	advProv := &fake{name: "advisor", responses: []*llm.Response{text("read parse.go next")}}
	a := &Agent{Provider: exec, Tools: tool.NewRegistry(), Model: "inception/mercury-2.5", AdvisorModel: "z-ai/glm-5.3"}
	a.Route = func(model string) (llm.Provider, string, error) {
		if model == "z-ai/glm-5.3" {
			return advProv, model, nil
		}
		return exec, model, nil
	}
	out, err := a.Advise(context.Background(), "stuck")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[advisor z-ai/glm-5.3]") || !strings.Contains(out, "read parse.go next") {
		t.Fatalf("out = %q", out)
	}
	if len(exec.requests) != 0 {
		t.Fatalf("executor provider was called: %d", len(exec.requests))
	}
	if len(advProv.requests) != 1 || advProv.requests[0].Model != "z-ai/glm-5.3" {
		t.Fatalf("advisor requests = %+v", advProv.requests)
	}
}

func TestAdvisorReplyUsesThinkingWhenContentEmpty(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockThinking, Text: "1) list the directory\n2) do not invent files"},
	}}
	if got := advisorReplyText(msg); !strings.Contains(got, "list the directory") {
		t.Fatalf("got %q", got)
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
