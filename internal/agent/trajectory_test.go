package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func TestTrainScoreAndPersistPredictor(t *testing.T) {
	var samples []Sample
	for i := 0; i < 40; i++ {
		// Failing runs have many errors and no tests; passing ones few
		// errors and a test run.
		f := map[string]float64{"tool_calls": float64(10 + i%5), "tool_errors": float64(i % 3), "tests": 1, "error_rate": float64(i%3) / 10}
		if i%2 == 1 {
			f["tool_errors"] = float64(6 + i%4)
			f["tests"] = 0
			f["error_rate"] = 0.6
		}
		samples = append(samples, Sample{Features: f, Passed: i%2 == 0})
	}
	p, err := Train(samples)
	if err != nil {
		t.Fatal(err)
	}
	if p.Positives != 20 || p.Negatives != 20 || p.Accuracy < 0.9 || p.Baseline != 0.5 {
		t.Fatalf("predictor = %+v", p)
	}
	good := p.Score(map[string]float64{"tool_calls": 12, "tool_errors": 1, "tests": 1, "error_rate": 0.1})
	bad := p.Score(map[string]float64{"tool_calls": 12, "tool_errors": 8, "tests": 0, "error_rate": 0.6})
	if good < 0.8 || bad > 0.2 {
		t.Fatalf("good=%.2f bad=%.2f", good, bad)
	}
	if why := p.Explain(map[string]float64{"tool_calls": 12, "tool_errors": 8, "tests": 0, "error_rate": 0.6}, 2); !strings.Contains(why, "tool_errors=8") && !strings.Contains(why, "error_rate=0.6") && !strings.Contains(why, "tests=0") {
		t.Fatalf("explain = %q", why)
	}
	path := filepath.Join(t.TempDir(), "new", "dir", "p.json") // Save creates the directory
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	q, err := LoadPredictor(path)
	if err != nil || q == nil || math.Abs(q.Score(samples[0].Features)-p.Score(samples[0].Features)) > 1e-9 {
		t.Fatalf("reload: %v %+v", err, q)
	}
	if q, err := LoadPredictor(filepath.Join(t.TempDir(), "none.json")); q != nil || err != nil {
		t.Fatalf("missing file: %v %v", q, err)
	}
	if _, err := Train(samples[:1]); !errors.Is(err, ErrOneClass) {
		t.Fatalf("one class: %v", err)
	}
	if !math.IsNaN((*Predictor)(nil).Score(nil)) {
		t.Fatal("nil predictor should score NaN")
	}
}

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
		if e.Kind == EvNotice {
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
		if e.Kind == EvNotice {
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
