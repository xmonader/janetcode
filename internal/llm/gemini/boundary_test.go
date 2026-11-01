package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestFunctionCallWithoutANameIsAnError(t *testing.T) {
	p, _ := newTest(t, frames(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"fc-1","name":"","args":{}}}]},"finishReason":"STOP"}]}`))
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err == nil {
		t.Fatalf("nameless call returned response %+v", resp)
	}
}

func TestStreamValidatesRequestBeforeSending(t *testing.T) {
	p, s := newTest(t)
	_, err := p.Stream(context.Background(), llm.Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no messages") {
		t.Fatalf("err = %v, want missing messages", err)
	}
	if len(s.bodies) != 0 {
		t.Fatalf("invalid request reached the server %d times", len(s.bodies))
	}
}
