package eval

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// slowScripted is scripted with a delay on every model call, so a run
// lasts long enough to report progress.
type slowScripted struct{ scripted }

func (s *slowScripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	time.Sleep(60 * time.Millisecond)
	return s.scripted.Stream(ctx, req, emit)
}

// lockedBuffer is a bytes.Buffer safe for the runner's goroutines.
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

// TestRunnerReportsStartAndProgress: --eval printed nothing until a task
// finished, which on a real model was thirteen silent minutes. Each task
// now says when it starts and, while it runs, how long it has been going
// and how many tool calls it has made.
func TestRunnerReportsStartAndProgress(t *testing.T) {
	var log lockedBuffer
	r := &Runner{Log: &log, ProgressEvery: 20 * time.Millisecond, NewAgent: func(root, _ string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &slowScripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}, nil
	}}
	r.Run(context.Background(), []Task{{Name: "slow", Prompt: "p", Check: "true"}})
	out := log.String()
	if !strings.Contains(out, "start slow") {
		t.Fatalf("no start line:\n%s", out)
	}
	if !strings.Contains(out, "… slow: ") || !strings.Contains(out, "elapsed") || !strings.Contains(out, "tool call") {
		t.Fatalf("no progress line:\n%s", out)
	}
}

// TestReportExplainsColumns: the table's "cmp" column and a "?" cost were
// never explained.
func TestReportExplainsColumns(t *testing.T) {
	var b bytes.Buffer
	Report(&b, []Result{{Name: "a", Passed: true}})
	if !strings.Contains(b.String(), "cmp = compactions") || !strings.Contains(b.String(), "? = model has no price") {
		t.Fatalf("no legend:\n%s", b.String())
	}
}
