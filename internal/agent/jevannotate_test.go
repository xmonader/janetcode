package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func annEvents() []session.Event {
	msg := func(role llm.Role, text string) *llm.Message {
		m := llm.TextMessage(role, text)
		return &m
	}
	return []session.Event{
		{Seq: 1, Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Type: session.TypeUserMessage, Turn: 1, Message: msg(llm.RoleUser, "make the parser accept trailing commas")},
		{Seq: 3, Type: session.TypeToolCall, Turn: 1, CallID: "a", Name: "read_file", Arguments: `{"path":"parse.go"}`},
		{Seq: 4, Type: session.TypeToolResult, Turn: 1, CallID: "a", Output: "package parse\nfunc Parse()"},
		{Seq: 5, Type: session.TypeToolCall, Turn: 1, CallID: "b", Name: "bash", Arguments: `{"command":"go test ./..."}`},
		{Seq: 6, Type: session.TypeToolResult, Turn: 1, CallID: "b", Output: "FAIL parse", IsError: true},
		{Seq: 7, Type: session.TypeTurnEnd, Turn: 1},
		{Seq: 8, Type: session.TypeTurnStart, Turn: 2},
		{Seq: 9, Type: session.TypeUserMessage, Turn: 2, Message: msg(llm.RoleUser, "now fix the test")},
		// A call whose result never made it to the log: the turn ended
		// first, or the process did.
		{Seq: 10, Type: session.TypeToolCall, Turn: 2, CallID: "c", Name: "edit_file", Arguments: `{"path":"parse.go"}`},
	}
}

func TestAnnotateTurnsFoldsCallsResultsAndFailures(t *testing.T) {
	turns := AnnotateTurns(annEvents())
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	one := turns[0]
	if one.Turn != 1 || one.Prompt != "make the parser accept trailing commas" {
		t.Fatalf("turn 1 = %+v", one)
	}
	if len(one.Steps) != 2 || one.Failed != 1 {
		t.Fatalf("turn 1 steps = %d failed = %d, want 2 and 1", len(one.Steps), one.Failed)
	}
	if one.Steps[1].Tool != "bash" || !one.Steps[1].Failed || one.Steps[1].Outcome != "FAIL parse" {
		t.Fatalf("turn 1 step 2 = %+v", one.Steps[1])
	}
	two := turns[1]
	if len(two.Steps) != 1 || two.Steps[0].Outcome != "(no result recorded)" {
		t.Fatalf("a call with no result should say so: %+v", two.Steps)
	}
	// A result whose call is in another turn must not be counted twice or
	// land on the wrong turn.
	if two.Failed != 0 {
		t.Fatalf("turn 2 failed = %d, want 0", two.Failed)
	}
}

func TestAnnotateStateRendersTheTurnAndElidesALongOne(t *testing.T) {
	state := AnnotateState("fix the parser", []StuckStep{
		{Tool: "read_file", Args: `{"path":"a.go"}`, Outcome: "package a"},
		{Tool: "bash", Args: `{"command":"go test"}`, Failed: true, Outcome: "FAIL"},
	}, "check failed")
	for _, want := range []string{"Request: fix the parser", "Harness verdict for the turn: check failed",
		"1. read_file", "2. bash", "ERROR: FAIL"} {
		if !strings.Contains(state, want) {
			t.Fatalf("state missing %q:\n%s", want, state)
		}
	}
	// A turn with no calls at all is a fact worth stating, not a blank.
	if empty := AnnotateState("explain this", nil, ""); !strings.Contains(empty, "none; the agent answered") {
		t.Fatalf("empty turn state = %q", empty)
	}
	// Past the window the middle is dropped and says how much was
	// dropped, so a long turn still reads as a long turn.
	var many []StuckStep
	for i := 0; i < AnnotateWindow+10; i++ {
		many = append(many, StuckStep{Tool: "grep", Args: `{"pattern":"x"}`, Outcome: "no match"})
	}
	long := AnnotateState("find it", many, "")
	if !strings.Contains(long, "further calls omitted") {
		t.Fatalf("a long turn should elide its middle:\n%s", long)
	}
	if strings.Count(long, ". grep") != AnnotateWindow {
		t.Fatalf("rendered %d calls, want %d", strings.Count(long, ". grep"), AnnotateWindow)
	}
}

func TestAnnotateOfReadsEveryAnswer(t *testing.T) {
	res := &jev.Result{Model: "jev-1.13.0", Answers: map[string]jev.Answer{
		qAnnOutcome:  {Kind: jev.Choice, Choice: "partial", Confidence: 0.77},
		qAnnProgress: {Kind: jev.Noul, Noul: 0.8},
		qAnnWasted:   {Kind: jev.Noul, Noul: 0.3},
		qAnnMisuse:   {Kind: jev.Noul, Noul: 0.1},
	}}
	a := AnnotateOf(res)
	if a.Outcome != "partial" || a.OutConf != 0.77 || a.Progress != 0.8 || a.Wasted != 0.3 || a.Misuse != 0.1 {
		t.Fatalf("AnnotateOf = %+v", a)
	}
	// A missing result is empty, not a panic: the driver writes an error
	// row for it.
	if got := AnnotateOf(nil); got.Outcome != "" {
		t.Fatalf("AnnotateOf(nil) = %+v", got)
	}
}
