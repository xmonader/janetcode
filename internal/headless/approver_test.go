package headless

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
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

// lockedBuffer is a bytes.Buffer two prompts may write to at once.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Parallel subagents whose profile bubbles approvals, and a background
// fork beside the main agent, ask at the same time. Both prompts were
// printed at once and each line typed went to whichever prompt happened
// to be waiting, so the "n" meant for `rm -rf build` could approve
// something else and the "y" meant for that approve it. One prompt is on
// the terminal at a time, and the line typed answers the one shown.
func TestStdinApproverAsksOneAtATimeAndAnswersTheOneShown(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	out := &lockedBuffer{}
	a := newStdinApprover(nil, out)
	a.src = pr
	a.interactive = func() bool { return true }
	defer a.Close()

	type result struct {
		name string
		d    agent.Decision
	}
	results := make(chan result, 2)
	for _, name := range []string{"alpha", "beta"} {
		go func(name string) {
			d, _, _ := a.ApproveWithReason(context.Background(), agent.ToolCall{Name: name, Input: []byte(`{}`)})
			results <- result{name, d}
		}(name)
	}
	shown := func() []string {
		var names []string
		for _, n := range []string{"alpha", "beta"} {
			if strings.Contains(out.String(), "Allow "+n) {
				names = append(names, n)
			}
		}
		return names
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(shown()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // give a second prompt time to appear, if it would
	first := shown()
	if len(first) != 1 {
		t.Fatalf("prompts on the terminal at once: %v\n%s", first, out.String())
	}
	if _, err := pw.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	r := <-results
	if r.name != first[0] || r.d != agent.Allow {
		t.Fatalf("the y typed for %s went to %s (%v)", first[0], r.name, r.d)
	}
	if _, err := pw.Write([]byte("n\n")); err != nil {
		t.Fatal(err)
	}
	if r := <-results; r.d != agent.Deny {
		t.Fatalf("second prompt %s = %v, want Deny", r.name, r.d)
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

// The exported constructor has to hand back something the caller can
// close: its reader goroutine owns stdin until it does.
func TestNewStdinApproverIsClosable(t *testing.T) {
	var a ClosableApprover = NewStdinApprover(nil, io.Discard)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing twice is safe, so a caller may close defensively.
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Run installs an approver of its own only when the agent has none, and
// takes it away again: a closed approver left behind would deny every
// prompt of a later run.
func TestRunLeavesNoClosedApproverBehind(t *testing.T) {
	ag := &agent.Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // watch returns as soon as it notices; no model is called
	_ = run(ctx, Options{Agent: ag, Stderr: io.Discard, Stdout: io.Discard, Watch: "false", WatchInterval: time.Millisecond}, newRecorder(Options{Stdout: io.Discard}, FormatText))
	if ag.Approver != nil {
		t.Fatalf("Approver = %v, want nil after the run", ag.Approver)
	}
}
