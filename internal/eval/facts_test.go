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
		{Seq: 2, Type: session.TypeToolResult, CallID: "c2", Name: "read_file", Arguments: `{"path":"main.go"}`, Output: "1\tpackage main\n2\t\n3\tfunc main() { run(loadConfig(\"cfg.json\")) }\n4\t\n5\tvar defaultTimeout = 30 * time.Second\n6\t}"},
		{Seq: 3, Type: session.TypeToolResult, Surface: true, Message: &gone},
		{Seq: 4, Type: session.TypeToolResult, CallID: "c3", Name: "bash", Arguments: `{"command":"ls"}`, Output: "a.go\n[exit code 0]"},
		{Seq: 5, Type: session.TypeToolResult, Surface: true, Message: &kept},
		{Seq: 6, Type: session.TypeCompactSummary, Shadowed: []int{3}},
	}
	probes := MineFacts(ev, 0)
	// Both shapes are produced: content questions that name something in
	// the answer's line, and positional ones that name only a line number.
	var content, positional int
	for _, p := range probes {
		if strings.Contains(p.Question, "what was on line") {
			positional++
		} else {
			content++
		}
	}
	if content == 0 || positional == 0 {
		t.Fatalf("one-sided probes (%d content, %d positional): %+v", content, positional, probes)
	}
	if !hasWant(probes, "y_test.go:9: boom happened here") {
		t.Fatalf("the failing line was not asked about: %+v", probes)
	}
	if !hasWant(probes, `func main() { run(loadConfig("cfg.json")) }`) {
		t.Fatalf("the read file's line was not asked about: %+v", probes)
	}
	for _, p := range probes {
		if strings.Contains(strings.ToLower(p.Question), strings.ToLower(p.Want)) {
			t.Fatalf("a question contains its own answer: %+v", p)
		}
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
	Score(&p, "The line was:  boom   happened here", "")
	if !p.Correct || p.Unknown || p.Grounded {
		t.Fatalf("score = %+v", p)
	}
	Score(&p, "The line was: boom happened here", "12\tboom happened here\n")
	if !p.Grounded {
		t.Fatalf("not grounded: %+v", p)
	}
	Score(&p, "I don't know, that output is no longer in my context.", "")
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

func hasWant(probes []Probe, want string) bool {
	for _, p := range probes {
		if p.Want == want {
			return true
		}
	}
	return false
}

// An answer repeated all over the session identifies nothing, so it is
// not asked about; nor is one short enough to be guessed.
func TestDistinctivenessFilter(t *testing.T) {
	common := "package main"
	var evs []session.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, session.Event{Type: session.TypeToolResult, CallID: "c", Name: "read_file", Output: common})
	}
	probes := distinctive([]Probe{
		{Question: "q1", Want: common},
		{Question: "q2", Want: "tiny"},
		{Question: "q3 mentioning parseTimeout", Want: "func parseTimeout() error { return nil }"},
		{Question: "q4", Want: "func parseTimeout() error { return nil }"},
	}, append(evs, session.Event{Type: session.TypeToolResult, Output: "func parseTimeout() error { return nil }"}))
	if len(probes) != 1 || probes[0].Question != "q3 mentioning parseTimeout" {
		t.Fatalf("filter kept %+v", probes)
	}
}

func TestInArchiveFindsWhatIsRecoverable(t *testing.T) {
	ar := agent.NewArchive(t.TempDir())
	if _, err := ar.PutSubject("c1", "bash", "go test", "--- FAIL: TestParse\n    got 3 want 4\n"); err != nil {
		t.Fatal(err)
	}
	if !inArchive(ar, "got 3 want 4") {
		t.Fatal("archived value reported missing")
	}
	// Whitespace is normalised, since a model re-wraps what it quotes.
	if !inArchive(ar, "got  3   want 4") {
		t.Fatal("whitespace should not decide recoverability")
	}
	if inArchive(ar, "never written anywhere") {
		t.Fatal("absent value reported present")
	}
	if inArchive(nil, "x") {
		t.Fatal("no archive should report nothing")
	}
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
