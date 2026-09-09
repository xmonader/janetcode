package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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
	back, err := ReadFile(out)
	if err != nil || len(back.Results) != 4 || back.Runs != 1 {
		t.Fatalf("results.json unreadable: %v %+v", err, back)
	}
	// The old bare-array format still loads.
	os.WriteFile(out, []byte(`[{"name":"x","passed":true},{"name":"x","run":1,"passed":false}]`), 0o644)
	if old, err := ReadFile(out); err != nil || len(old.Results) != 2 || old.Runs != 2 {
		t.Fatalf("old format: %v %+v", err, old)
	}
}

func TestRunnerOverridesForBenchmarks(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "limited.json", Task{Prompt: "write hello", Check: "true", MaxToolCalls: 1, Timeout: 1})
	tasks, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks[0].MaxToolCalls = 1
	newAgent := func(root string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}, nil
	}
	if rs := (&Runner{NewAgent: newAgent, IgnoreToolLimit: true, Timeout: time.Minute}).Run(context.Background(), tasks); !rs[0].Passed {
		t.Fatalf("override ignored: %+v", rs[0])
	}
	// A runner timeout replaces the task's own.
	if rs := (&Runner{NewAgent: newAgent, IgnoreToolLimit: true, Timeout: time.Nanosecond}).Run(context.Background(), tasks); rs[0].Passed || !strings.Contains(rs[0].Reason, "timed out after 1ns") {
		t.Fatalf("timeout not applied: %+v", rs[0])
	}
}

func TestRepeatedRunsAndSummary(t *testing.T) {
	calls := 0
	r := &Runner{Runs: 3, NewAgent: func(root string) (*agent.Agent, error) {
		calls++
		return &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Price: &llm.Price{Input: 1, Output: 2}}, nil
	}}
	res := r.Run(context.Background(), []Task{
		{Name: "a", Prompt: "p", Check: "test -f hello.txt"},
		{Name: "b", Prompt: "p", Check: "test -f nope.txt"},
	})
	if len(res) != 6 || calls != 6 {
		t.Fatalf("%d results, %d agents", len(res), calls)
	}
	if res[0].Name != "a" || res[2].Run != 2 || res[3].Name != "b" || res[5].Run != 2 {
		t.Fatalf("order: %+v", res)
	}
	s := Summarize(res)
	if s.Tasks != 2 || s.Runs != 3 || s.Passed != 3 || s.Total != 6 || s.PassRate != 0.5 || !s.CostKnown || s.CostPerSolved <= 0 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Lo >= 0.5 || s.Hi <= 0.5 || s.Lo < 0.18 || s.Hi > 0.82 {
		t.Fatalf("wilson interval = [%.2f, %.2f]", s.Lo, s.Hi)
	}
	if s.ByTask[0].Rate != 1 || s.ByTask[1].Rate != 0 {
		t.Fatalf("by task = %+v", s.ByTask)
	}
	var buf bytes.Buffer
	if passed := Report(&buf, res); passed != 3 || !strings.Contains(buf.String(), "3/3   a") || !strings.Contains(buf.String(), "0/3   b") || !strings.Contains(buf.String(), "2 task(s) × 3 run(s)") {
		t.Fatalf("passed=%d report:\n%s", passed, buf.String())
	}
}

func TestCompareIsPairedByTask(t *testing.T) {
	mk := func(name string, run int, passed bool, cost float64) Result {
		return Result{Name: name, Run: run, Passed: passed, CostUSD: cost, CostKnown: true}
	}
	base := []Result{mk("a", 0, true, 1), mk("a", 1, true, 1), mk("b", 0, false, 1), mk("b", 1, false, 1), mk("c", 0, true, 1), mk("c", 1, false, 1), mk("gone", 0, true, 1)}
	cur := []Result{mk("a", 0, true, 1), mk("a", 1, true, 1), mk("b", 0, true, 1), mk("b", 1, true, 1), mk("c", 0, false, 1), mk("c", 1, false, 1), mk("new", 0, true, 1)}
	c := Compare(base, cur)
	if c.Shared != 3 || c.Delta < 0.16 || c.Delta > 0.17 { // (0 + 1 − 0.5) / 3
		t.Fatalf("comparison = %+v", c)
	}
	if strings.Join(c.Improved, ",") != "b" || strings.Join(c.Regressed, ",") != "c" || strings.Join(c.OnlyBase, ",") != "gone" || strings.Join(c.OnlyCurrent, ",") != "new" {
		t.Fatalf("lists: %+v", c)
	}
	if c.Lo > c.Delta || c.Hi < c.Delta || c.Significant() {
		t.Fatalf("interval [%.2f, %.2f] around %.2f should include 0 with three tasks", c.Lo, c.Hi, c.Delta)
	}
	if !c.CostKnown || c.CostDelta == 0 {
		t.Fatalf("cost delta = %+v", c)
	}
	// Twenty tasks that all flip to passing is a clear signal.
	var b2, c2 []Result
	for i := 0; i < 20; i++ {
		name := string(rune('a' + i))
		b2 = append(b2, mk(name, 0, i%2 == 0, 1))
		c2 = append(c2, mk(name, 0, true, 1))
	}
	if c := Compare(b2, c2); !c.Significant() || c.Lo <= 0 {
		t.Fatalf("clear improvement not significant: %+v", c)
	}
	// Deterministic intervals.
	if lo1, hi1 := bootstrapCI([]float64{0, 1, -0.5, 0.5}, 500, 1); true {
		if lo2, hi2 := bootstrapCI([]float64{0, 1, -0.5, 0.5}, 500, 1); lo1 != lo2 || hi1 != hi2 {
			t.Fatal("bootstrap not reproducible")
		}
	}
	var buf bytes.Buffer
	WriteComparison(&buf, c)
	if out := buf.String(); !strings.Contains(out, "pass-rate delta +16.7%") || !strings.Contains(out, "improved:  b") || !strings.Contains(out, "regressed: c") {
		t.Fatalf("report:\n%s", out)
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

func TestFoldCompaction(t *testing.T) {
	u := llm.Usage{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 50}
	ev := []session.Event{
		{Type: session.TypeToolCall, Name: "read_file"},
		{Type: session.TypeCompactSummary, Usage: &u},
		{Type: session.TypeToolCall, Name: "expand_output"},
		{Type: session.TypeCompactSummary},
	}
	c := FoldCompaction(ev)
	if c.Count != 2 || c.Expands != 1 || c.Summary.CacheReadTokens != 900 {
		t.Fatalf("compaction = %+v", c)
	}
	s := Summarize([]Result{{Name: "a", Passed: true, Usage: u, Compaction: c}, {Name: "b", Usage: u}})
	if s.Context != 2000 || s.ContextPerSolved != 2000 || s.Compactions != 2 {
		t.Fatalf("summary = %+v", s)
	}
	if line := summaryLine(s); !strings.Contains(line, "context 2.0k") || !strings.Contains(line, "2 compaction(s)") {
		t.Fatalf("line = %s", line)
	}
}
