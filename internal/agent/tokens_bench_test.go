package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/tool"
)

// benchAgent is a session the size of a long real one: a hundred
// messages, the full tool registry and the real system prompt. The TUI
// calls EstimateTokens twice per render while the user is typing, so
// this is on the keystroke path.
func benchAgent(b *testing.B) *Agent {
	b.Helper()
	root := b.TempDir()
	seen := &tool.Seen{}
	a := &Agent{
		System: prompt.System(root),
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: root, Seen: seen},
			tool.WriteFile{Root: root, Seen: seen},
			tool.EditFile{Root: root, Seen: seen},
			tool.Bash{Root: root, Cache: &tool.ShellCache{}},
			tool.ListDir{Root: root},
			tool.Glob{Root: root},
			tool.Grep{Root: root},
			tool.FetchURL{},
		),
	}
	a.Tools.Add(&TodoTool{Parent: a})
	a.Tools.Add(&Script{Parent: a, Root: root})
	a.Tools.Add(&ExpandOutput{})
	body := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, \"ok\") }\n", 12)
	for i := 0; i < 100; i++ {
		if i%2 == 0 {
			a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
				{Type: llm.BlockToolResult, ToolUseID: fmt.Sprintf("t%d", i), Text: body},
			}})
			continue
		}
		a.Messages = append(a.Messages, llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Looking at the handler now."},
			{Type: llm.BlockToolUse, ID: fmt.Sprintf("t%d", i), Name: "read_file",
				Input: json.RawMessage(`{"path":"internal/server/handler.go"}`)},
		}})
	}
	return a
}

// TestToolEstimateFollowsTheRegistry: the tool-definition estimate is
// cached between calls, so the cache has to notice every way the visible
// set of tools can change.
func TestToolEstimateFollowsTheRegistry(t *testing.T) {
	root := t.TempDir()
	a := &Agent{System: "s", Tools: tool.NewRegistry(tool.ReadFile{Root: root})}
	one := a.EstimateTokens()
	if a.EstimateTokens() != one {
		t.Fatal("the estimate is not stable when nothing changed")
	}
	a.Tools.Add(tool.Grep{Root: root})
	two := a.EstimateTokens()
	if two <= one {
		t.Fatalf("adding a tool did not grow the estimate: %d → %d", one, two)
	}
	a.Tools.AddDeferred(tool.Glob{Root: root})
	if got := a.EstimateTokens(); got != two {
		t.Fatalf("a deferred tool changed the estimate: %d → %d", two, got)
	}
	a.Tools.Activate("glob")
	three := a.EstimateTokens()
	if three <= two {
		t.Fatalf("activating a deferred tool did not grow the estimate: %d → %d", two, three)
	}
	a.Tools.Remove("grep")
	if got := a.EstimateTokens(); got >= three {
		t.Fatalf("removing a tool did not shrink the estimate: %d → %d", three, got)
	}
	a.Tools.Intent = true
	if got := a.EstimateTokens(); got <= 0 {
		t.Fatal("no estimate with intent on")
	} else if got == three {
		t.Fatal("switching the intent field on did not change the estimate")
	}
}

func BenchmarkEstimateTokensHistory100(b *testing.B) {
	a := benchAgent(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.EstimateTokens()
	}
}

func BenchmarkSystemPrompt(b *testing.B) {
	a := benchAgent(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.systemPrompt()
	}
}

func BenchmarkToolDefs(b *testing.B) {
	a := benchAgent(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Tools.Defs()
	}
}
