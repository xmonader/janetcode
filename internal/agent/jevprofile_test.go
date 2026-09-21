package agent

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// TestClassifyCall walks every branch the classifier has: each tool family,
// each shell head family, a check, a git subcommand on both sides, an
// unparseable argument blob and an unknown tool.
func TestClassifyCall(t *testing.T) {
	cases := []struct {
		name, args string
		want       Phase
	}{
		{"read_file", `{"path":"a.go"}`, PhaseExplore},
		{"list_dir", `{"path":"."}`, PhaseExplore},
		{"glob", `{"pattern":"*.go"}`, PhaseExplore},
		{"grep", `{"pattern":"Foo"}`, PhaseExplore},
		{"repo_map", `{}`, PhaseExplore},
		{"expand_output", `{}`, PhaseExplore},
		{"write_file", `{"path":"a.go","content":"x"}`, PhaseEdit},
		{"edit_file", `{"path":"a.go"}`, PhaseEdit},
		{"todo", `{"todos":[]}`, PhaseOther},
		{"memory", `{}`, PhaseOther},
		{"execute_code", `{"code":"read_file('a')"}`, PhaseOther},
		{"bash", `{"command":"go test ./..."}`, PhaseVerify},
		{"bash", `{"command":"go build ./..."}`, PhaseVerify},
		{"bash", `{"command":"ls -la"}`, PhaseExplore},
		{"bash", `{"command":"cat a.go | head -20"}`, PhaseExplore},
		{"bash", `{"command":"/usr/bin/find . -name '*.go'"}`, PhaseExplore},
		{"bash", `{"command":"GOFLAGS=-mod=mod cat go.mod"}`, PhaseExplore},
		{"bash", `{"command":"mkdir -p x"}`, PhaseEdit},
		{"bash", `{"command":"rm -f x"}`, PhaseEdit},
		{"bash", `{"command":"gofmt -w a.go"}`, PhaseEdit},
		{"bash", `{"command":"git status"}`, PhaseExplore},
		{"bash", `{"command":"git commit -m x"}`, PhaseOther},
		{"bash", `{"command":"curl http://x"}`, PhaseOther},
		{"bash", `not json`, PhaseOther},
		{"bash", `{"command":""}`, PhaseOther},
		{"no_such_tool", `{}`, PhaseOther},
	}
	for _, c := range cases {
		if got := ClassifyCall(c.name, c.args); got != c.want {
			t.Errorf("ClassifyCall(%q, %q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func assistant(seq int, text string, in, cache int) session.Event {
	return session.Event{
		Seq: seq, Type: session.TypeAssistant,
		Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: text}}},
		Usage:   &llm.Usage{InputTokens: in, CacheReadTokens: cache},
	}
}

func toolPair(seq int, id, name, args, out string) []session.Event {
	return []session.Event{
		{Seq: seq, Type: session.TypeToolCall, CallID: id, Name: name, Arguments: args},
		{Seq: seq + 1, Type: session.TypeToolResult, CallID: id, Output: out},
	}
}

// TestProfileRunCountsPhasesAndFirstEdit checks the two ceilings that do not
// need the stale accounting: the phase split and the calls before the first
// edit.
func TestProfileRunCountsPhasesAndFirstEdit(t *testing.T) {
	var ev []session.Event
	ev = append(ev, assistant(1, "looking", 100, 0))
	ev = append(ev, toolPair(2, "a", "read_file", `{"path":"a.go"}`, "x")...)
	ev = append(ev, assistant(4, "still looking", 200, 50))
	ev = append(ev, toolPair(5, "b", "grep", `{"pattern":"Foo"}`, "y")...)
	ev = append(ev, assistant(7, "editing", 300, 100))
	ev = append(ev, toolPair(8, "c", "edit_file", `{"path":"a.go"}`, "ok")...)
	ev = append(ev, assistant(10, "checking", 400, 150))
	ev = append(ev, toolPair(11, "d", "bash", `{"command":"go test ./..."}`, "ok")...)

	p := ProfileRun("m", ev, 2)
	if p.ModelCalls != 4 || p.ToolCalls != 4 {
		t.Fatalf("calls: model %d tool %d, want 4 and 4", p.ModelCalls, p.ToolCalls)
	}
	if p.ToolPhase[PhaseExplore] != 2 || p.ToolPhase[PhaseEdit] != 1 || p.ToolPhase[PhaseVerify] != 1 {
		t.Errorf("tool phases: %v", p.ToolPhase)
	}
	if p.ModelPhase[PhaseExplore] != 2 || p.ModelPhase[PhaseEdit] != 1 || p.ModelPhase[PhaseVerify] != 1 {
		t.Errorf("model phases: %v", p.ModelPhase)
	}
	// The edit is issued by the third model call, so two came before it.
	if p.CallsBeforeFirstEdit != 3 || p.EditingRuns != 1 {
		t.Errorf("before first edit: %d over %d runs, want 3 over 1", p.CallsBeforeFirstEdit, p.EditingRuns)
	}
	if p.InputFirst != 100 || p.InputLast != 550 {
		t.Errorf("input first %d last %d, want 100 and 550", p.InputFirst, p.InputLast)
	}
	if p.InputTokens != 100+250+400+550 {
		t.Errorf("input tokens %d", p.InputTokens)
	}
	if got := p.InputGrowth; got != 150 {
		t.Errorf("growth %v, want 150", got)
	}
}

// TestProfileRunStaleAccounting is the item-3 ceiling: a result whose path is
// read again is never stale, a result quoted later is never stale, and a
// result nobody touches is charged for every call past staleAfter that still
// carried it.
func TestProfileRunStaleAccounting(t *testing.T) {
	const quotedLine = "func QuotedSymbolNobodyElseHas() error"
	keptBody := "func KeptSymbolNobodyElseHas() error { return nil }\n"
	quotedBody := quotedLine + "\n"
	deadBody := "func DeadSymbolNobodyElseHas() error { return nil }\n"
	var ev []session.Event
	ev = append(ev, assistant(1, "", 10, 0))
	ev = append(ev, toolPair(2, "reread", "read_file", `{"path":"kept.go"}`, keptBody)...)
	ev = append(ev, toolPair(4, "quoted", "read_file", `{"path":"quoted.go"}`, quotedBody)...)
	ev = append(ev, toolPair(6, "dead", "read_file", `{"path":"dead.go"}`, deadBody)...)
	for i := 0; i < 5; i++ {
		ev = append(ev, assistant(10+i, "", 10, 0))
	}
	// The path is touched again, and the other file's line is quoted.
	ev = append(ev, toolPair(20, "reread2", "read_file", `{"path":"kept.go"}`, keptBody)...)
	ev = append(ev, assistant(22, "I saw "+quotedLine, 10, 0))

	p := ProfileRun("m", ev, 1)
	if p.Results != 4 {
		t.Fatalf("results %d, want 4", p.Results)
	}
	// dead.go is the only one never referenced again; the final read of
	// kept.go is itself never referenced again either.
	if p.StaleResults != 2 {
		t.Errorf("stale results %d, want 2 (dead.go and the last kept.go read)", p.StaleResults)
	}
	if p.ToolOutputStale <= 0 {
		t.Errorf("stale tokens %d, want a positive charge for dead.go", p.ToolOutputStale)
	}
	if p.ToolOutputStale >= p.ToolOutputPaid {
		t.Errorf("stale %d must be under paid %d", p.ToolOutputStale, p.ToolOutputPaid)
	}
}

// TestProfileRunNoEditsLeavesTheMeanUndefined guards the divide-by-zero the
// report would otherwise print as 0.0 calls before an edit that never came.
func TestProfileRunNoEditsLeavesTheMeanUndefined(t *testing.T) {
	ev := []session.Event{assistant(1, "hello", 10, 0)}
	p := ProfileRun("m", ev, 6)
	if p.EditingRuns != 0 || p.CallsBeforeFirstEdit != 0 {
		t.Errorf("a run with no edit reported %d over %d", p.CallsBeforeFirstEdit, p.EditingRuns)
	}
	if p.GrowthRuns != 0 {
		t.Errorf("a one-call run cannot have a growth slope")
	}
}
