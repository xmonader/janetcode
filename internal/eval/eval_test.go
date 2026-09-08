package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// scripted answers every task the same way: write hello.txt, then say done.
type scripted struct{ fail bool }

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleUser && last.Blocks[0].Type == llm.BlockText && !s.fail {
		return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hello world"}`)}}}, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
	}
	if emit != nil {
		emit(llm.Event{Type: llm.EventTextDelta, Text: "done: 42"})
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done: 42"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 50, OutputTokens: 5}}, nil
}

func writeTask(t *testing.T, dir, name string, task Task) {
	t.Helper()
	data, _ := json.Marshal(task)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFilterAndRun(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "b-write.json", Task{Prompt: "write hello", Check: `grep -q "hello world" hello.txt && grep -q 42 "$EVAL_OUTPUT"`, Tags: []string{"files"}})
	writeTask(t, dir, "a-answer.json", Task{Name: "answer", Prompt: "say 42", Check: `grep -q 42 "$EVAL_OUTPUT"`, Files: map[string]string{"sub/x.txt": "seed"}})
	writeTask(t, dir, "c-strict.json", Task{Prompt: "write hello", Check: "true", MaxToolCalls: 0, Timeout: 30})
	writeTask(t, dir, "d-limit.json", Task{Prompt: "write hello", Check: "true", MaxToolCalls: 0})
	tasks, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 4 || tasks[0].Name != "answer" || tasks[1].Name != "b-write" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if got := Filter(tasks, []string{"files", "answer"}); len(got) != 2 {
		t.Fatalf("filter = %+v", got)
	}
	tasks[3].MaxToolCalls = 1
	tasks[3].Check = "false"
	var seed string
	r := &Runner{Jobs: 2, NewAgent: func(root string) (*agent.Agent, error) {
		if b, err := os.ReadFile(filepath.Join(root, "sub", "x.txt")); err == nil {
			seed = string(b)
		}
		return &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Price: &llm.Price{Input: 1, Output: 2}}, nil
	}}
	results := r.Run(context.Background(), tasks)
	if seed != "seed" {
		t.Fatal("setup files were not written before the run")
	}
	if !results[0].Passed || !results[1].Passed || !results[2].Passed || results[3].Passed {
		t.Fatalf("results = %+v", results)
	}
	if results[1].ToolCalls != 1 || results[1].Turns != 2 || !results[1].CostKnown || results[1].CostUSD <= 0 || results[1].Duration <= 0 {
		t.Fatalf("write result = %+v", results[1])
	}
	if !strings.Contains(results[3].Reason, "used 1 tool calls, limit 1") && !strings.Contains(results[3].Reason, "check failed") {
		t.Fatalf("reason = %q", results[3].Reason)
	}
	var buf bytes.Buffer
	if passed := Report(&buf, results); passed != 3 || !strings.Contains(buf.String(), "3/4 passed") || !strings.Contains(buf.String(), "FAIL d-limit") {
		t.Fatalf("passed=%d report:\n%s", passed, buf.String())
	}
	out := filepath.Join(dir, "results.json")
	if err := WriteJSON(out, results); err != nil {
		t.Fatal(err)
	}
	var back []Result
	if data, _ := os.ReadFile(out); json.Unmarshal(data, &back) != nil || len(back) != 4 {
		t.Fatal("results.json unreadable")
	}
}

func TestLoadRejectsBadTasks(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "x.json", Task{Prompt: "p"})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "prompt and check are required") {
		t.Fatalf("err = %v", err)
	}
	writeTask(t, dir, "x.json", Task{Name: "same", Prompt: "p", Check: "true"})
	writeTask(t, dir, "y.json", Task{Name: "same", Prompt: "p", Check: "true"})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("empty dir accepted")
	}
}

func TestRunFailsWhenModelErrors(t *testing.T) {
	r := &Runner{NewAgent: func(root string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &scripted{fail: true}, Tools: tool.NewRegistry(), Model: "m"}, nil
	}}
	res := r.Run(context.Background(), []Task{{Name: "t", Prompt: "p", Check: "test -f hello.txt"}})
	if res[0].Passed || !strings.Contains(res[0].Reason, "check failed") || res[0].CostKnown {
		t.Fatalf("res = %+v", res[0])
	}
}
