package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// slowTool sleeps and records overlap so the test can prove concurrency.
type slowTool struct {
	name     string
	mutating bool
	active   *int32
	maxSeen  *int32
	order    *[]string
	mu       *sync.Mutex
}

func (s slowTool) Name() string            { return s.name }
func (s slowTool) Description() string     { return "slow" }
func (s slowTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s slowTool) Mutating() bool          { return s.mutating }
func (s slowTool) Run(ctx context.Context, in json.RawMessage) (string, error) {
	n := atomic.AddInt32(s.active, 1)
	for {
		m := atomic.LoadInt32(s.maxSeen)
		if n <= m || atomic.CompareAndSwapInt32(s.maxSeen, m, n) {
			break
		}
	}
	select {
	case <-time.After(80 * time.Millisecond):
	case <-ctx.Done():
		atomic.AddInt32(s.active, -1)
		return "", ctx.Err()
	}
	atomic.AddInt32(s.active, -1)
	s.mu.Lock()
	*s.order = append(*s.order, s.name+":"+string(in))
	s.mu.Unlock()
	return "done " + string(in), nil
}

func TestReadOnlyToolsRunInParallelThenMutatingInOrder(t *testing.T) {
	var active, maxSeen int32
	var order []string
	var mu sync.Mutex
	mk := func(name string, mutating bool) slowTool {
		return slowTool{name, mutating, &active, &maxSeen, &order, &mu}
	}
	reg := tool.NewRegistry(mk("r", false), mk("w", true))
	call := func(id, name, in string) llm.Block {
		return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(in)}
	}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			call("1", "w", `"a"`), call("2", "r", `"b"`), call("3", "r", `"c"`), call("4", "w", `"d"`), call("5", "r", `"e"`),
		}}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a := &Agent{Provider: f, Tools: reg, Model: "m", AutoApprove: true}
	start := time.Now()
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	// 3 reads in parallel (~80ms) + 2 writes sequential (~160ms) < 5*80ms.
	if elapsed > 350*time.Millisecond {
		t.Fatalf("took %s; tools did not overlap", elapsed)
	}
	if atomic.LoadInt32(&maxSeen) < 2 {
		t.Fatalf("max concurrent = %d; reads should overlap", maxSeen)
	}
	// Results are in the model's order regardless of completion order.
	res := a.Messages[2].Blocks
	for i, want := range []string{"1", "2", "3", "4", "5"} {
		if res[i].ToolUseID != want || res[i].IsError {
			t.Fatalf("result %d = %+v", i, res[i])
		}
	}
	// Mutating tools ran after all reads, and in order.
	n := len(order)
	if n != 5 || order[n-2] != `w:"a"` || order[n-1] != `w:"d"` {
		t.Fatalf("order = %v", order)
	}
}

func TestParallelCancelFillsAllResults(t *testing.T) {
	var active, maxSeen int32
	var order []string
	var mu sync.Mutex
	reg := tool.NewRegistry(slowTool{"r", false, &active, &maxSeen, &order, &mu})
	f := &fake{responses: []*llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "1", Name: "r", Input: json.RawMessage(`1`)},
		{Type: llm.BlockToolUse, ID: "2", Name: "r", Input: json.RawMessage(`2`)},
	}}, StopReason: llm.StopToolUse}}}
	a := &Agent{Provider: f, Tools: reg, Model: "m"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := a.Run(ctx, "go", nil); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	res := a.Messages[2].Blocks
	if len(res) != 2 || !res[0].IsError || !res[1].IsError {
		t.Fatalf("results = %+v", res)
	}
}

// registeringTool adds a tool to the registry while it runs, the way
// tool_search activating a deferred tool or an MCP server connecting
// mid-turn changes the registry's version during a parallel batch.
type registeringTool struct {
	reg  *tool.Registry
	name string
}

func (r registeringTool) Name() string          { return r.name }
func (registeringTool) Description() string     { return "registers a tool" }
func (registeringTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (registeringTool) Mutating() bool          { return false }
func (r registeringTool) Run(context.Context, json.RawMessage) (string, error) {
	r.reg.Add(panicTool{})
	return "registered", nil
}

// Every read-only call in a batch measures the context for the oversized
// check, and that measure caches the tool definitions' size by registry
// version. A call that changed the version mid-batch had the parallel
// calls after it all rewriting the cache at once: a data race under -race.
func TestParallelCallsMeasureTheContextWithoutARace(t *testing.T) {
	reg := tool.NewRegistry()
	var uses []llm.Block
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("reg%d", i)
		reg.Add(registeringTool{reg: reg, name: name})
		uses = append(uses, llm.Block{Type: llm.BlockToolUse, ID: fmt.Sprint(i), Name: name, Input: json.RawMessage(`{}`)})
	}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: uses}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a := &Agent{Provider: f, Tools: reg, Model: "m"}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
}
