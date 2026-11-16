package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func TestCriticWarnsOncePerTurn(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < 12; i++ {
		responses = append(responses, toolCall(fmt.Sprint(i), "write_file", fmt.Sprintf(`{"path":"f%d.txt","content":"x"}`, i)))
	}
	responses = append(responses, text("done"))
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	// A predictor that dislikes edits: every write lowers the score.
	p := &Predictor{Names: FeatureNames, Mean: make([]float64, len(FeatureNames)), Std: make([]float64, len(FeatureNames)), Weight: make([]float64, len(FeatureNames))}
	for i, n := range FeatureNames {
		p.Std[i] = 1
		if n == "edits" {
			p.Weight[i] = -1
		}
	}
	a.Predictor = p
	var notices []string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice && !strings.HasPrefix(e.Text, "checkpoint ") {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "trajectory critic") || !strings.Contains(notices[0], "edits=5") {
		t.Fatalf("notices = %v", notices)
	}
	// The note reached the model with the fifth result, and only there.
	count := 0
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && strings.Contains(b.Text, "[harness] trajectory critic") {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("critic notes in history = %d", count)
	}
	if s := a.LastScore(); math.IsNaN(s) || s >= predictorWarnAt {
		t.Fatalf("last score = %v", s)
	}
	feat := a.Trajectory()
	if feat["edits"] != 12 || feat["files_edited"] != 12 || feat["tool_calls"] != 12 || feat["minutes"] < 0 {
		t.Fatalf("features = %v", feat)
	}
	// Off switch.
	b, _ := newAgent(t, &fake{responses: responses}, nil)
	b.AutoApprove, b.Predictor, b.Off = true, p, map[string]bool{"critic": true}
	n := 0
	if err := b.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice && !strings.HasPrefix(e.Text, "checkpoint ") {
			n++
		}
	}); err != nil || n != 0 {
		t.Fatalf("critic off: err=%v notices=%d", err, n)
	}
}

func TestFoldTrajectoryCountsFilesAndSuccessfulEdits(t *testing.T) {
	ev := []session.Event{
		{Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Type: session.TypeToolCall, Turn: 1, Name: "edit_file", Arguments: `{"path":"a.go","old_str":"x"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "t1", Name: "edit_file", Arguments: `{"path":"a.go","old_str":"x"}`},
		{Type: session.TypeToolCall, Turn: 1, Name: "edit_file", Arguments: `{"path":"a.go","old_str":"y"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "t2", Name: "edit_file", Arguments: `{"path":"a.go","old_str":"y"}`},
		{Type: session.TypeToolCall, Turn: 1, Name: "write_file", Arguments: `{"path":"b.go"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "t3", Name: "write_file", Arguments: `{"path":"b.go"}`, IsError: true},
	}
	f := FoldTrajectory(ev)
	// Two edits to a.go are one file, and the failed write is neither an
	// edit nor a file.
	if f["files_edited"] != 1 {
		t.Fatalf("files_edited = %v (want the distinct paths)", f["files_edited"])
	}
	if f["edits"] != 2 {
		t.Fatalf("edits = %v (want the successful ones)", f["edits"])
	}
	if f["tool_errors"] != 1 {
		t.Fatalf("tool_errors = %v", f["tool_errors"])
	}
}

func TestTrajectoryScopedToTheRunningTurn(t *testing.T) {
	ev := []session.Event{
		{Type: session.TypeTurnStart, Turn: 1},
		{Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Type: session.TypeToolCall, Turn: 1, Name: "read_file", Arguments: `{"path":"a"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "t1", Name: "read_file", Arguments: `{"path":"a"}`},
		{Type: session.TypeTurnEnd, Turn: 1},
		{Type: session.TypeTurnStart, Turn: 2},
		{Type: session.TypeStepStart, Turn: 2, Step: 1},
		{Type: session.TypeToolCall, Turn: 2, Name: "grep", Arguments: `{"pattern":"x"}`},
		{Type: session.TypeToolResult, Turn: 2, CallID: "t2", Name: "grep", Arguments: `{"pattern":"x"}`},
	}
	a := &Agent{Log: &session.Memory{Ev: ev}}
	a.RestoreFromLog()
	got := a.Trajectory()
	if got["tool_calls"] != 1 || got["model_calls"] != 1 {
		t.Fatalf("turn 2 features = %v (they must not include turn 1)", got)
	}
}
