package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

var errRejected = errors.New("openrouter stream error: Upstream error from Groq: Tool call validation failed: parameters for tool apply_patch missing properties: 'input' (HTTP 500)")

// A provider refusing a malformed tool call is not the end of the turn: the
// model is told and calls again.
func TestRejectedToolCallIsSentBack(t *testing.T) {
	f := &fake{
		errs:      []error{errRejected},
		responses: []*llm.Response{nil, text("done")},
	}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.requests))
	}
	got := diagLastText(f.requests[1])
	if !strings.Contains(got, "rejected by the provider before it ran") || !strings.Contains(got, "Tool call validation failed") {
		t.Fatalf("second request ends with %q", got)
	}
	if n := a.Stats().RejectedCalls; n != 1 {
		t.Fatalf("RejectedCalls = %d, want 1", n)
	}
}

// The recovery is bounded: past the cap the turn fails with the typed error.
func TestRejectedToolCallsAreBounded(t *testing.T) {
	f := &fake{errs: []error{errRejected, errRejected, errRejected, errRejected, errRejected}}
	a, _ := newAgent(t, f, nil)
	err := a.Run(context.Background(), "go", nil)
	if !errors.Is(err, llm.ErrToolCallRejected) {
		t.Fatalf("err = %v, want ErrToolCallRejected", err)
	}
	if len(f.requests) != 1+maxRejectedCalls {
		t.Fatalf("model calls = %d, want %d", len(f.requests), 1+maxRejectedCalls)
	}
}

func TestRejectedCallNoteNamesTheToolsForAnUnknownTool(t *testing.T) {
	unknown := errors.New("Tool call validation failed: attempted to call tool 'search' which was not in request.tools")
	if n := rejectedCallNote(unknown, "bash, read_file"); !strings.Contains(n, "The only tools available are: bash, read_file.") {
		t.Fatalf("note = %q", n)
	}
	bad := errors.New("Failed to parse tool call arguments as JSON")
	if n := rejectedCallNote(bad, "bash, read_file"); strings.Contains(n, "only tools available") {
		t.Fatalf("a parse failure named the tools: %q", n)
	}
}
