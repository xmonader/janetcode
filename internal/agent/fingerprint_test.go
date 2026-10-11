package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// fingerprintFake answers every canary with the same word and counts the
// calls.
type fingerprintFake struct {
	reply string
	calls atomic.Int32
}

func (f *fingerprintFake) Name() string { return "broker" }

func (f *fingerprintFake) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	f.calls.Add(1)
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, f.reply)}, nil
}

func fingerprintNotices(a *Agent) []string {
	var out []string
	a.fingerprintSession(context.Background(), func(e Event) {
		if e.Kind == EvNotice {
			out = append(out, e.Text)
		}
	})
	return out
}

// Off unless a directory is set; once per session when it is; a route
// whose model names another maker than the requested one is reported.
func TestFingerprintSessionOnce(t *testing.T) {
	f := &fingerprintFake{reply: "Moonshot AI"}
	a := &Agent{Provider: f, Model: "claude-sonnet-4-5"}
	if n := fingerprintNotices(a); len(n) != 0 || f.calls.Load() != 0 {
		t.Fatalf("off: notices %q calls %d", n, f.calls.Load())
	}
	a.FingerprintDir = t.TempDir()
	n := fingerprintNotices(a)
	if len(n) != 1 || !strings.Contains(n[0], "moonshot") {
		t.Fatalf("notices = %q", n)
	}
	calls := f.calls.Load()
	if n := fingerprintNotices(a); len(n) != 0 || f.calls.Load() != calls {
		t.Fatalf("second turn: notices %q, calls %d -> %d", n, calls, f.calls.Load())
	}
}
