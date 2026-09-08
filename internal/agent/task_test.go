package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// scriptedByPrompt answers each conversation according to its first user
// message, so parent and children can share one provider.
type scriptedByPrompt struct {
	mu    sync.Mutex
	calls int
}

func (s *scriptedByPrompt) Name() string { return "fake" }
func (s *scriptedByPrompt) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	first := req.Messages[0].Text()
	last := req.Messages[len(req.Messages)-1]
	switch {
	case strings.HasPrefix(first, "explore"):
		// child: read a.txt once, then report
		if last.Role == llm.RoleUser && len(last.ToolUses()) == 0 && last.Blocks[0].Type != llm.BlockToolResult {
			for _, d := range req.Tools {
				if d.Name == "write_file" || d.Name == "task" {
					return nil, errorf("child must not see %s", d.Name)
				}
			}
			return toolCall("c1", "read_file", `{"path":"a.txt"}`), nil
		}
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "REPORT: a.txt says "+strings.TrimSpace(last.Blocks[0].Text)), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
	default:
		// parent: fan out two tasks, then summarise
		if last.Role == llm.RoleUser && last.Blocks[0].Type == llm.BlockToolResult {
			var texts []string
			for _, b := range last.Blocks {
				texts = append(texts, b.Text)
			}
			return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "SUMMARY: "+strings.Join(texts, " | ")), StopReason: llm.StopEndTurn}, nil
		}
		r := toolCall("p1", "task", `{"description":"one","prompt":"explore one"}`)
		r.Message.Blocks = append(r.Message.Blocks, llm.Block{Type: llm.BlockToolUse, ID: "p2", Name: "task", Input: json.RawMessage(`{"description":"two","prompt":"explore two"}`)})
		return r, nil
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }
func errorf(f string, a ...any) error {
	return strErr(strings.TrimSpace(strings.ReplaceAll(f, "%s", a[0].(string))))
}

func TestTaskRunsSubagentsInParallel(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	prov := &scriptedByPrompt{}
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, task)

	var events []Event
	if err := parent.Run(context.Background(), "go", func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	final := parent.Messages[len(parent.Messages)-1].Text()
	if !strings.HasPrefix(final, "SUMMARY: ") || strings.Count(final, "REPORT: a.txt says") != 2 {
		t.Fatalf("final = %q", final)
	}
	if !strings.Contains(final, "[subagent \"one\": 1 tool calls") {
		t.Fatalf("missing footer: %q", final)
	}
	if u := task.Usage(); u.InputTokens < 200 || u.OutputTokens < 20 {
		t.Fatalf("child usage not aggregated: %+v", u)
	}
	// Children's tool events must not leak into the parent's stream.
	for _, e := range events {
		if e.Call != nil && e.Call.Name == "read_file" {
			t.Fatal("child event leaked to parent")
		}
	}
	if prov.calls != 6 {
		t.Fatalf("provider calls = %d, want 2 parent + 2 per child", prov.calls)
	}
}

func TestTaskRejectsBadInput(t *testing.T) {
	task := &Task{Parent: &Agent{Tools: tool.NewRegistry()}}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"  "}`)); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := (&Task{}).Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"y"}`)); err == nil {
		t.Fatal("missing parent accepted")
	}
}
