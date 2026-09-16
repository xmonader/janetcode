package critic

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
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
	// Explain must decline the same predictors Score declines, not panic:
	// it indexes Weight by Names, which a truncated critic.json can leave
	// disagreeing.
	if why := (*Predictor)(nil).Explain(nil, 2); why != "" {
		t.Fatalf("nil predictor explained %q", why)
	}
	if why := (&Predictor{Names: FeatureNames}).Explain(map[string]float64{"tests": 0}, 2); why != "" {
		t.Fatalf("an unfitted predictor explained %q", why)
	}
}

// The cross-validated accuracy is what decides whether a trained
// predictor is kept at all. A fold whose training half turns out to be
// one class is skipped, but its test samples were still in the divisor,
// counted as wrong — so the reported accuracy sat below the real one,
// worst of all in the case this predictor is trained in: few failures.
func TestCrossValidationScoresOnlyTheFoldsItRan(t *testing.T) {
	// Twenty samples with a single failure. With five folds, four of them
	// train on a set that still holds that failure and one does not, so
	// one fold is skipped.
	var samples []Sample
	for i := 0; i < 20; i++ {
		pass := i != 3
		f := map[string]float64{"tool_errors": 0, "tests": 1}
		if !pass {
			f = map[string]float64{"tool_errors": 9, "tests": 0}
		}
		samples = append(samples, Sample{Features: f, Passed: pass})
	}
	p, err := Train(samples)
	if err != nil {
		t.Fatal(err)
	}
	// Four folds of four samples each are scored (the fifth is skipped),
	// and this data separates perfectly, so every scored sample is right.
	if p.Accuracy < 0.99 {
		t.Fatalf("accuracy = %.3f, want ~1 over the folds that ran (16 of 20 samples)", p.Accuracy)
	}
	if p.Accuracy > 1 {
		t.Fatalf("accuracy = %.3f, above 1", p.Accuracy)
	}
}
