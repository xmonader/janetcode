package sleepfold

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
)

// The sleep fold turns a session's event log into the structured record
// the consolidation pass reads: goal and outcome, the harness's
// interventions, what was refused, the user's asks, and the files the
// session changed — every line carrying the seq it came from.
func TestFold(t *testing.T) {
	events := []session.Event{
		{Seq: 1, Type: session.TypeGoalCreate, Name: "g1", Output: "fix the parser"},
		{Seq: 2, Type: session.TypeUserMessage, Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "fix the parser, please"}}}},
		{Seq: 3, Type: session.TypeToolCall, Name: "write_file", Arguments: `{"path":"parser.go","content":"..."}`},
		{Seq: 4, Type: session.TypeToolCall, Name: "edit_file", Arguments: `{"path":"parser.go","old":"a","new":"b"}`},
		{Seq: 5, Type: session.TypeToolCall, Name: "edit_file", Arguments: `{"path":"lexer.go","old":"a","new":"b"}`},
		{Seq: 6, Type: session.TypeToolResult, Name: "bash", Denied: true, Output: "Denied by permission rule\nrest"},
		{Seq: 11, Type: session.TypeToolResult, Name: "bash", IsError: true, Output: "exit 1: undefined: devMode\nmore detail"},
		{Seq: 7, Type: session.TypeCritic, Output: "trajectory looks stuck: 3 failed edits"},
		{Seq: 8, Type: session.TypeVerdict, Name: "pass", Output: "green"},
		{Seq: 9, Type: session.TypeGoalPhase, Name: "g1", Reason: goalComplete, Output: "parser fixed"},
		{Seq: 10, Type: session.TypeUserMessage, Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "<harness>\nsynthetic\n</harness>", Synthetic: true}}}},
		{Seq: 12, Type: session.TypeGate, Name: "make test", IsError: true, Output: "VERSION and version.go must match"},
		{Seq: 13, Type: session.TypeGate, Name: "make test"},
	}
	in := Fold(events)
	if in.Goal != "fix the parser" || in.GoalOutcome != goalComplete {
		t.Fatalf("goal = %+v", in)
	}
	if strings.Join(in.Touched, ",") != "parser.go,lexer.go" {
		t.Fatalf("touched = %v (deduped, in order)", in.Touched)
	}
	kinds := map[string]int{}
	var userLines, refusedLines int
	for _, f := range in.Facts {
		kinds[f.Kind]++
		if f.Kind == "user" {
			userLines++
			if strings.Contains(f.Text, "harness") {
				t.Fatalf("synthetic text folded as a user line: %q", f.Text)
			}
		}
		if f.Kind == "refused" {
			refusedLines++
			if strings.Contains(f.Text, "rest") {
				t.Fatalf("refusal carried past its first line: %q", f.Text)
			}
		}
		if f.Seq <= 0 {
			t.Fatalf("fact without its seq: %+v", f)
		}
	}
	if kinds["critic"] != 1 || kinds["verdict"] != 1 || kinds["goal"] != 1 || kinds["failure"] != 1 || userLines != 1 || refusedLines != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
	if kinds["verify"] != 1 || kinds["verify-pass"] != 1 {
		t.Fatalf("gate pair missing: kinds = %v", kinds)
	}
	var fail, pass memory.SleepFact
	for _, f := range in.Facts {
		switch f.Kind {
		case "verify":
			fail = f
		case "verify-pass":
			pass = f
		}
	}
	if fail.Name != "make test" || fail.Text != "VERSION and version.go must match" || fail.Seq != 12 {
		t.Fatalf("verify fail = %+v", fail)
	}
	if pass.Name != "make test" || pass.Text != "make test" || pass.Seq != 13 {
		t.Fatalf("verify pass = %+v", pass)
	}
}

// A session with nothing in it folds to a record sleep will skip.
func TestFoldEmpty(t *testing.T) {
	in := Fold(nil)
	if in.Goal != "" || len(in.Facts) != 0 || len(in.Touched) != 0 {
		t.Fatalf("in = %+v", in)
	}
}

// Capping keeps the facts in the order they happened: the goal, user
// lines and the verdict interleave as the session had them.
func TestCapSleepFactsKeepsChronology(t *testing.T) {
	var facts []memory.SleepFact
	for i := 1; i <= sleepFactCap+10; i++ {
		kind := "user"
		if i%10 == 0 {
			kind = "verdict"
		}
		facts = append(facts, memory.SleepFact{Seq: i, Kind: kind})
	}
	got := capSleepFacts(facts)
	if len(got) != sleepFactCap {
		t.Fatalf("kept %d, want %d", len(got), sleepFactCap)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq < got[i-1].Seq {
			t.Fatalf("fact %d (seq %d) after seq %d", i, got[i].Seq, got[i-1].Seq)
		}
	}
}
