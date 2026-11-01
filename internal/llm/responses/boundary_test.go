package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

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

func TestFunctionCallWithoutANameIsAnError(t *testing.T) {
	p, _ := newTest(t, events(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_A","name":"","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	))
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err == nil {
		t.Fatalf("nameless call returned response %+v", resp)
	}
}

func TestMalformedOutputItemIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event string
	}{
		{"added", `{"type":"response.output_item.added","output_index":0,"item":"not-an-object"}`},
		{"done", `{"type":"response.output_item.done","output_index":0,"item":"not-an-object"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTest(t, events(tc.event, `{"type":"response.completed","response":{"status":"completed"}}`))
			p.c.MaxRetries = 0
			resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
			if err == nil {
				t.Fatalf("malformed item returned response %+v", resp)
			}
		})
	}
}
