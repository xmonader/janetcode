package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// beforeClosing is the history without the closing reply a stopped turn
// ends with.
func beforeClosing(msgs []llm.Message) llm.Message {
	last := msgs[len(msgs)-1]
	if last.Role == llm.RoleAssistant && strings.Contains(last.Text(), continueHint) {
		return msgs[len(msgs)-2]
	}
	return last
}

func TestDeterministicClosing(t *testing.T) {
	todos := []Todo{{Text: "write parser", Status: TodoDone}, {Text: "wire the CLI", Status: "in_progress"}}
	cases := []struct {
		name  string
		f     limitFacts
		wants []string
		nots  []string
	}{
		{"nothing done", limitFacts{why: "its step limit (3 model calls)"},
			[]string{"step limit (3 model calls)", "No files were changed", "was not written down as a plan", continueHint}, []string{"verified"}},
		{"failed check and todos", limitFacts{why: "its cost budget", edited: []string{"a.go", "b.go"}, verdict: VerdictCheckFailed, lastCheck: "go test ./...", todos: todos},
			[]string{"cost budget", "- write parser", "Changed: a.go, b.go", "last check failed: `go test ./...`", "wire the CLI (in progress)", continueHint}, []string{"was not written down"}},
		{"edited unchecked", limitFacts{why: "x", edited: []string{"a.go"}, verdict: VerdictUnverified},
			[]string{"Nothing has verified these changes yet"}, nil},
		{"verified", limitFacts{why: "x", edited: []string{"a.go"}, verdict: VerdictVerified},
			[]string{"last check that ran passed"}, nil},
	}
	for _, c := range cases {
		got := deterministicClosing(c.f)
		for _, w := range c.wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", c.name, w, got)
			}
		}
		for _, w := range c.nots {
			if strings.Contains(got, w) {
				t.Errorf("%s: unexpected %q", c.name, w)
			}
		}
	}
}

// The tool-limit stop makes one more model call for Done/Left; its text
// ends with our continue line. A failed call falls back to the facts.
func TestToolLimitClosingReply(t *testing.T) {
	run := func(f *fake, off bool) *Agent {
		a, _ := newAgent(t, f, nil)
		a.MaxToolCalls = 1
		if off {
			a.Off = map[string]bool{"steplimit": true}
		}
		err := a.Run(context.Background(), "read twice", nil)
		if !errors.Is(err, ErrToolLimit) {
			t.Fatalf("err = %v", err)
		}
		return a
	}
	calls := func() []*llm.Response {
		return []*llm.Response{toolCall("r1", "read_file", `{"path":"a.txt"}`), toolCall("r2", "read_file", `{"path":"a.txt"}`)}
	}
	f := &fake{responses: append(calls(), text("**Done** read it. **Left** nothing."))}
	a := run(f, false)
	last := a.Messages[len(a.Messages)-1]
	if last.Role != llm.RoleAssistant || !strings.HasPrefix(last.Text(), "**Done** read it.") || !strings.HasSuffix(last.Text(), continueHint) {
		t.Fatalf("closing = %q", last.Text())
	}
	req := f.requests[len(f.requests)-1]
	if !strings.Contains(req.Messages[len(req.Messages)-1].Text(), "cannot call any more tools") {
		t.Fatal("the closing call did not carry the instruction")
	}
	for _, m := range a.Messages {
		if strings.Contains(m.Text(), "cannot call any more tools") {
			t.Fatal("the instruction must not stay in the history")
		}
	}
	failing := &fake{responses: calls(), errs: []error{nil, nil, errors.New("boom")}}
	a = run(failing, false)
	if last := a.Messages[len(a.Messages)-1]; !strings.Contains(last.Text(), "**Done**") || !strings.Contains(last.Text(), "tool-call limit") {
		t.Fatalf("fallback = %q", last.Text())
	}
	off := &fake{responses: calls()}
	a = run(off, true)
	if len(off.requests) != 2 || a.Messages[len(a.Messages)-1].Role != llm.RoleUser {
		t.Fatal("off switch ignored")
	}
}
