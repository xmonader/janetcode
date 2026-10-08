package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func cutOff(s string) *llm.Response {
	r := text(s)
	r.StopReason = llm.StopMaxTokens
	return r
}

// A reply the output limit cut off before any tool call is not the end of
// the turn: the model is told and asked again.
func TestCutReplyIsSentBack(t *testing.T) {
	f := &fake{responses: []*llm.Response{cutOff(""), text("done")}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.requests))
	}
	if got := diagLastText(f.requests[1]); !strings.Contains(got, "cut off at the output token limit") {
		t.Fatalf("second request ends with %q", got)
	}
}

// The send-back is bounded: a model that keeps running out ends the turn.
func TestCutRepliesAreBounded(t *testing.T) {
	f := &fake{responses: []*llm.Response{cutOff(""), cutOff(""), cutOff(""), text("never reached")}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1+maxCutReplies {
		t.Fatalf("model calls = %d, want %d", len(f.requests), 1+maxCutReplies)
	}
}
