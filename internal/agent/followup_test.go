package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestClampFollowup(t *testing.T) {
	if got := clampFollowup(`"run the tests next"`); got != "run the tests next" {
		t.Fatalf("got %q", got)
	}
	if got := clampFollowup("one"); got != "" {
		t.Fatalf("short: %q", got)
	}
	long := "a b c d e f g h i j k l m n"
	if got := clampFollowup(long); got != "a b c d e f g h i j k l" {
		t.Fatalf("long: %q", got)
	}
}

func TestSuggestFollowupIsACheapSideQuery(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("run the failing test")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "big-model", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "fix the parser"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: strings.Repeat("secret chain of thought ", 40)},
			{Type: llm.BlockText, Text: "I edited parse.go"},
		}},
	}}
	a.RouteCheap = "cheap-model"
	got, err := a.SuggestFollowup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "run the failing test" {
		t.Fatalf("got %q", got)
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	req := f.requests[0]
	if req.System != followupSystem {
		t.Fatalf("system = %q", req.System)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("tools leaked into the side query: %d", len(req.Tools))
	}
	if req.Model != "cheap-model" {
		t.Fatalf("model = %q, want the cheap one", req.Model)
	}
	if strings.Contains(req.System, "janetcode") {
		t.Fatal("full system prompt leaked")
	}
	blob := req.Messages[0].Text() + req.Messages[1].Text()
	if strings.Contains(blob, "secret chain of thought") {
		t.Fatal("thinking leaked into the side query")
	}
}
