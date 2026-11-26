package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestResultCarriesCacheMisses(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
		}}, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 100, CacheReadTokens: 9000}},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 9300}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
	var out bytes.Buffer
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	if err := Run(context.Background(), Options{Agent: ag, Prompt: "go", Stdout: &out, Stderr: io.Discard, Stdin: devnull, Format: FormatStreamJSON}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var res map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		t.Fatal(err)
	}
	cm, ok := res["cache_misses"].(map[string]any)
	if !ok || cm["tokens"].(float64) < 1024 || cm["calls"].(float64) != 1 {
		t.Fatalf("cache_misses = %v", res["cache_misses"])
	}
	if _, ok := cm["by_cause"].(map[string]any); !ok {
		t.Fatalf("by_cause missing: %v", cm)
	}
	// Fewer than two calls: omitted.
	one := &agent.Agent{Model: "m"}
	if r := newRecorder(Options{Stdout: io.Discard, Agent: one}, FormatJSON).result(one, nil); r.CacheMisses != nil {
		t.Fatalf("expected omitted, got %+v", r.CacheMisses)
	}
}
