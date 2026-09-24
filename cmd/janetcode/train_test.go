package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/eval"
)

func TestTrainCriticFromEvalFiles(t *testing.T) {
	dir := t.TempDir()
	var pass, fail []eval.Result
	for i := 0; i < 12; i++ {
		pass = append(pass, eval.Result{Name: "p", Passed: true, Features: map[string]float64{"tool_calls": 8, "tool_errors": float64(i % 2), "tests": 1}})
		fail = append(fail, eval.Result{Name: "f", Passed: false, Features: map[string]float64{"tool_calls": 30, "tool_errors": float64(6 + i%3), "tests": 0}})
	}
	if err := eval.WriteFile(filepath.Join(dir, "a.json"), eval.File{Results: pass}); err != nil {
		t.Fatal(err)
	}
	if err := eval.WriteFile(filepath.Join(dir, "b.json"), eval.File{Results: fail}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "critic.json")
	var w bytes.Buffer
	if err := trainCritic([]string{dir}, out, &w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "trained on 24 runs (12 passed, 12 failed) from 2 files") || !strings.Contains(w.String(), "saved to") {
		t.Fatalf("output:\n%s", w.String())
	}
	p, err := agent.LoadPredictor(out)
	if err != nil || p == nil || p.Score(fail[0].Features) > 0.3 || p.Score(pass[0].Features) < 0.7 {
		t.Fatalf("saved predictor: %v %+v", err, p)
	}
	// Indistinguishable runs train to the baseline and are not saved.
	var same []eval.Result
	for i := 0; i < 10; i++ {
		same = append(same, eval.Result{Name: "s", Passed: i < 7, Features: map[string]float64{"tool_calls": 5}})
	}
	if err := eval.WriteFile(filepath.Join(dir, "same.json"), eval.File{Results: same}); err != nil {
		t.Fatal(err)
	}
	unsaved := filepath.Join(t.TempDir(), "unsaved.json")
	w.Reset()
	if err := trainCritic([]string{filepath.Join(dir, "same.json")}, unsaved, &w); err != nil || !strings.Contains(w.String(), "not saved") {
		t.Fatalf("baseline: %v\n%s", err, w.String())
	}
	if _, err := os.Stat(unsaved); err == nil {
		t.Fatal("a critic no better than the baseline was saved")
	}
	// One class refuses; files without features are named as such.
	if err := trainCritic([]string{filepath.Join(dir, "a.json")}, out, &w); err == nil || !strings.Contains(err.Error(), "both passing and failing") {
		t.Fatalf("one class: %v", err)
	}
	old := filepath.Join(dir, "old.json")
	os.WriteFile(old, []byte(`{"results":[{"name":"x","passed":true}]}`), 0o644)
	if err := trainCritic([]string{old}, out, &w); err == nil || !strings.Contains(err.Error(), "no runs with trajectory features") {
		t.Fatalf("no features: %v", err)
	}
}
