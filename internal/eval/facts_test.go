package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestMineFactsFromShadowedResults(t *testing.T) {
	gone := llm.ToolResultMessage(llm.ToolResult("c1", "x", false), llm.ToolResult("c2", "y", false))
	kept := llm.ToolResultMessage(llm.ToolResult("c3", "z", false))
	ev := []session.Event{
		{Seq: 1, Type: session.TypeToolResult, CallID: "c1", Name: "bash", Arguments: `{"command":"go test ./..."}`, Output: "ok  \texample.com/x\t0.01s\n--- FAIL: TestY (0.00s)\n    y_test.go:9: boom happened here\n[exit code 1]"},
		{Seq: 2, Type: session.TypeToolResult, CallID: "c2", Name: "read_file", Arguments: `{"path":"main.go"}`, Output: "1\tpackage main\n2\t\n3\tfunc main() { run(loadConfig(\"cfg.json\")) }\n4\t}"},
		{Seq: 3, Type: session.TypeToolResult, Surface: true, Message: &gone},
		{Seq: 4, Type: session.TypeToolResult, CallID: "c3", Name: "bash", Arguments: `{"command":"ls"}`, Output: "a.go\n[exit code 0]"},
		{Seq: 5, Type: session.TypeToolResult, Surface: true, Message: &kept},
		{Seq: 6, Type: session.TypeCompactSummary, Shadowed: []int{3}},
	}
	probes := MineFacts(ev, 0)
	if len(probes) != 3 {
		t.Fatalf("probes = %+v", probes)
	}
	if probes[0].Want != "1" || probes[1].Want != "y_test.go:9: boom happened here" || !strings.Contains(probes[2].Question, "line 3") || probes[2].Want != `func main() { run(loadConfig("cfg.json")) }` {
		t.Fatalf("probes = %+v", probes)
	}
	for _, p := range probes {
		if strings.Contains(p.Question, "ls") && strings.Contains(p.Question, "`ls`") {
			t.Fatal("a result the model still has verbatim was probed")
		}
	}
	if got := MineFacts(ev, 1); len(got) != 1 {
		t.Fatalf("max ignored: %d", len(got))
	}
	p := Probe{Want: "boom happened here"}
	Score(&p, "The line was:  boom   happened here")
	if !p.Correct || p.Unknown {
		t.Fatalf("score = %+v", p)
	}
	Score(&p, "I don't know, that output is no longer in my context.")
	if p.Correct || !p.Unknown {
		t.Fatalf("unknown = %+v", p)
	}
}

// answers replies UNKNOWN to every probe.
type answers struct{ seen int }

func (a *answers) Name() string { return "fake" }
func (a *answers) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	a.seen++
	for _, d := range req.Tools {
		if d.Name != "expand_output" {
			return nil, context.Canceled
		}
	}
	if emit != nil {
		emit(llm.Event{Type: llm.EventTextDelta, Text: "UNKNOWN"})
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "UNKNOWN"), StopReason: llm.StopEndTurn}, nil
}

func TestProbeFactsRunsEachOnACopy(t *testing.T) {
	prov := &answers{}
	ag := &agent.Agent{Provider: prov, Model: "m", Tools: tool.NewRegistry(tool.WriteFile{Root: t.TempDir()}), Archive: agent.NewArchive(t.TempDir()),
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "[compacted]"), llm.TextMessage(llm.RoleAssistant, "ok")}}
	fp := ProbeFacts(context.Background(), ag, []Probe{{Question: "q1", Want: "a"}, {Question: "q2", Want: "b"}}, false)
	if fp.Asked != 2 || fp.Unknown != 2 || fp.Correct != 0 || prov.seen != 2 {
		t.Fatalf("probe = %+v seen=%d", fp, prov.seen)
	}
	if len(ag.Messages) != 2 {
		t.Fatalf("probing changed the agent's history: %d messages", len(ag.Messages))
	}
}
