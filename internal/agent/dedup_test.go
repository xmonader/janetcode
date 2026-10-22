package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// kvTools is a read tool and a write tool over one shared map, with the
// read counting its runs so a test can prove a duplicate never executed.
type kvTools struct {
	mu      sync.Mutex
	content map[string]string
	reads   int
}

func newKVTools() *kvTools {
	return &kvTools{content: map[string]string{"a": "old", "b": "old"}}
}

type kv struct{ K, V string }

func (k *kvTools) read() funcTool {
	return funcTool{name: "r", run: func(in json.RawMessage) (string, error) {
		var p kv
		_ = json.Unmarshal(in, &p)
		k.mu.Lock()
		defer k.mu.Unlock()
		k.reads++
		return k.content[p.K], nil
	}}
}

func (k *kvTools) write() funcTool {
	return funcTool{name: "w", mutating: true, run: func(in json.RawMessage) (string, error) {
		var p kv
		_ = json.Unmarshal(in, &p)
		k.mu.Lock()
		defer k.mu.Unlock()
		k.content[p.K] = p.V
		return "ok", nil
	}}
}

func (k *kvTools) readCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reads
}

func dedupCall(id, name, in string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(in)}
}

// runDedupBatch runs one turn whose single step makes the given calls and
// returns the step's tool results, in the model's order.
func runDedupBatch(t *testing.T, k *kvTools, emit func(Event), calls ...llm.Block) []llm.Block {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: calls}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(k.write(), k.read()), Model: "m", AutoApprove: true}
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	return a.Messages[2].Blocks
}

// The same read twice in one batch runs once; the second result points at
// the first. Key order in the input JSON does not make a call distinct.
func TestIdenticalReadsInABatchRunOnce(t *testing.T) {
	k := newKVTools()
	var (
		evMu    sync.Mutex
		dedupEv bool
	)
	res := runDedupBatch(t, k, func(e Event) {
		evMu.Lock()
		defer evMu.Unlock()
		if e.Kind == EvToolResult && e.Output == dedupedResult {
			dedupEv = true
		}
	},
		dedupCall("1", "r", `{"K":"a"}`),
		dedupCall("2", "r", `{"K":"a"}`),
		dedupCall("3", "r", `{"K":"b"}`),
	)
	if n := k.readCount(); n != 2 {
		t.Fatalf("read ran %d times, want 2 (one per distinct input)", n)
	}
	// One result per call, in the model's order, none an error.
	for i, want := range []string{"1", "2", "3"} {
		if res[i].ToolUseID != want || res[i].IsError {
			t.Fatalf("result %d = %+v", i, res[i])
		}
	}
	if res[1].Text != dedupedResult {
		t.Fatalf("the duplicate read got %q, want the dedup pointer", res[1].Text)
	}
	if res[0].Text != "old" || res[2].Text != "old" {
		t.Fatalf("results = %q, %q", res[0].Text, res[2].Text)
	}
	evMu.Lock()
	defer evMu.Unlock()
	if !dedupEv {
		t.Fatal("no result event carried the dedup pointer")
	}
}

// Semantically identical input dedups even when the bytes differ.
func TestDedupComparesCanonicalInput(t *testing.T) {
	k := newKVTools()
	res := runDedupBatch(t, k, nil,
		dedupCall("1", "r", `{"K":"a","V":""}`),
		dedupCall("2", "r", `{ "V":"", "K":"a" }`),
	)
	if n := k.readCount(); n != 1 {
		t.Fatalf("read ran %d times, want 1: key order and whitespace are not a different call", n)
	}
	if res[1].Text != dedupedResult {
		t.Fatalf("result 1 = %q, want the dedup pointer", res[1].Text)
	}
}

// A mutating call between two identical reads invalidates the first: the
// second read runs and sees the edit.
func TestReadEditReadIsNotDeduped(t *testing.T) {
	k := newKVTools()
	res := runDedupBatch(t, k, nil,
		dedupCall("1", "r", `{"K":"a"}`),
		dedupCall("2", "w", `{"K":"a","V":"new"}`),
		dedupCall("3", "r", `{"K":"a"}`),
	)
	if n := k.readCount(); n != 2 {
		t.Fatalf("read ran %d times, want 2: a read after an edit must see the edit", n)
	}
	if res[0].Text != "old" || res[2].Text != "new" {
		t.Fatalf("results = %q, %q; want old then new", res[0].Text, res[2].Text)
	}
}

// Duplicates after the mutating barrier dedup among themselves: the first
// read after the write runs (and sees the write), the second points at it.
func TestDuplicatesAfterTheBarrierDedupAmongThemselves(t *testing.T) {
	k := newKVTools()
	res := runDedupBatch(t, k, nil,
		dedupCall("1", "w", `{"K":"a","V":"new"}`),
		dedupCall("2", "r", `{"K":"a"}`),
		dedupCall("3", "r", `{"K":"a"}`),
	)
	if n := k.readCount(); n != 1 {
		t.Fatalf("read ran %d times, want 1", n)
	}
	if res[1].Text != "new" {
		t.Fatalf("the read after the write got %q, want the post-edit content", res[1].Text)
	}
	if res[2].Text != dedupedResult || res[2].IsError {
		t.Fatalf("result 2 = %+v, want the dedup pointer", res[2])
	}
}

// Mutating calls are never deduped: two identical writes both run.
func TestMutatingDuplicatesAreNeverDeduped(t *testing.T) {
	writes := 0
	w := funcTool{name: "w", mutating: true, run: func(in json.RawMessage) (string, error) {
		writes++
		return "ok", nil
	}}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			dedupCall("1", "w", `{"K":"a","V":"1"}`),
			dedupCall("2", "w", `{"K":"a","V":"1"}`),
		}}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(w), Model: "m", AutoApprove: true}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("the identical write ran %d times, want 2", writes)
	}
	for i, res := range a.Messages[2].Blocks {
		if strings.Contains(res.Text, "deduped") || res.IsError {
			t.Fatalf("result %d = %+v; a mutating call must run for real", i, res)
		}
	}
}

// Every call in a mixed batch gets exactly one result, in the model's
// order, whether it ran or was deduped.
func TestDedupKeepsTheHistoryPaired(t *testing.T) {
	k := newKVTools()
	res := runDedupBatch(t, k, nil,
		dedupCall("1", "r", `{"K":"a"}`),
		dedupCall("2", "r", `{"K":"a"}`),
		dedupCall("3", "w", `{"K":"a","V":"new"}`),
		dedupCall("4", "r", `{"K":"a"}`),
		dedupCall("5", "r", `{"K":"a"}`),
	)
	if len(res) != 5 {
		t.Fatalf("got %d results for 5 calls", len(res))
	}
	for i, want := range []string{"1", "2", "3", "4", "5"} {
		if res[i].ToolUseID != want {
			t.Fatalf("result %d answers call %s, want %s: %+v", i, res[i].ToolUseID, want, res)
		}
	}
	if k.readCount() != 2 {
		t.Fatalf("read ran %d times, want 2 (one before the write, one after)", k.readCount())
	}
	wantText := []string{"old", dedupedResult, "ok", "new", dedupedResult}
	for i, want := range wantText {
		if res[i].Text != want {
			t.Fatalf("result %d = %q, want %q (all: %+v)", i, res[i].Text, want, res)
		}
	}
}

// A duplicate of a parallel read that failed failed too: it used to be
// answered "[deduped …]" with no error flag, which reads as a success.
func TestDuplicateOfAFailedParallelReadIsAnError(t *testing.T) {
	bad := funcTool{name: "r", run: func(json.RawMessage) (string, error) {
		return "", errors.New("no such key")
	}}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			dedupCall("1", "r", `{"K":"x"}`),
			dedupCall("2", "r", `{"K":"x"}`),
		}}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(bad), Model: "m", AutoApprove: true}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := a.Messages[2].Blocks
	if !res[0].IsError || !res[1].IsError {
		t.Fatalf("results = %+v", res)
	}
}
