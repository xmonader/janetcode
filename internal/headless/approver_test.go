package headless

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

func pipedApprover(src io.Reader) *stdinApprover {
	a := newStdinApprover(nil, io.Discard)
	a.src = src
	a.interactive = func() bool { return true }
	return a
}

// A prompt abandoned by its context must not leave a reader behind that
// swallows the answer meant for the next prompt.
func TestStdinApproverCancelledPromptDoesNotStealNextAnswer(t *testing.T) {
	pr, pw := io.Pipe()
	a := pipedApprover(pr)
	defer a.Close()
	call := agent.ToolCall{Name: "bash", Input: []byte(`{}`)}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if d, _, err := a.ApproveWithReason(ctx, call); d != agent.Deny || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prompt: decision=%v err=%v", d, err)
	}

	go func() { _, _ = pw.Write([]byte("y\n")) }()
	d, _, err := a.ApproveWithReason(context.Background(), call)
	if err != nil || d != agent.Allow {
		t.Fatalf("second prompt: decision=%v err=%v, want Allow", d, err)
	}
}

// Two answers arriving in one read are both delivered, in order.
func TestStdinApproverKeepsReadAhead(t *testing.T) {
	a := pipedApprover(strings.NewReader("y\nn use the config dir\n"))
	defer a.Close()
	call := agent.ToolCall{Name: "write_file", Input: []byte(`{}`)}
	if d, _, err := a.ApproveWithReason(context.Background(), call); err != nil || d != agent.Allow {
		t.Fatalf("first: %v %v", d, err)
	}
	d, reason, err := a.ApproveWithReason(context.Background(), call)
	if err != nil || d != agent.Deny || reason != "use the config dir" {
		t.Fatalf("second: %v %q %v", d, reason, err)
	}
	// Input is exhausted: every later prompt denies instead of hanging.
	for i := 0; i < 2; i++ {
		if d, _, err := a.ApproveWithReason(context.Background(), call); err != nil || d != agent.Deny {
			t.Fatalf("after EOF: %v %v", d, err)
		}
	}
}

func TestStdinApproverDeniesAfterClose(t *testing.T) {
	pr, _ := io.Pipe()
	a := pipedApprover(pr)
	a.Close()
	if d, _, err := a.ApproveWithReason(context.Background(), agent.ToolCall{Name: "bash"}); err != nil || d != agent.Deny {
		t.Fatalf("after Close: %v %v", d, err)
	}
}
