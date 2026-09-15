// Package critic is the harness looking at its own work: the fitted
// predictor that scores a turn's trajectory features, and /why, which
// reads the session ledger back and says in plain words what the harness
// last did and why. Features and events in, numbers and sentences out —
// no agent, and no model call.
package critic

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FeatureNames is the feature order; Trajectory fills every one.
var FeatureNames = []string{
	"tool_calls", "tool_errors", "error_rate", "model_calls", "reads", "edits",
	"files_edited", "tests", "test_failures", "edits_since_test", "max_repeat",
	"cycle_rounds", "input_ktokens", "output_ktokens", "compactions", "minutes",
}

// Sample is one trajectory with its outcome, for training.
type Sample struct {
	Features map[string]float64
	Passed   bool
}

// Predictor is a logistic model over standardised trajectory features.
type Predictor struct {
	Names     []string  `json:"names"`
	Mean      []float64 `json:"mean"`
	Std       []float64 `json:"std"`
	Weight    []float64 `json:"weight"`
	Bias      float64   `json:"bias"`
	Samples   int       `json:"samples"`
	Positives int       `json:"positives"`
	Negatives int       `json:"negatives"`
	Accuracy  float64   `json:"cv_accuracy"` // 5-fold cross-validated
	Baseline  float64   `json:"baseline"`    // accuracy of always guessing the majority
	When      time.Time `json:"when"`
}

// ErrOneClass is returned when the samples do not contain both outcomes.
var ErrOneClass = errors.New("training needs both passing and failing runs")

const (
	trainIters = 3000
	trainRate  = 0.05
	trainL2    = 0.01
)

// Train fits a predictor to the samples. It refuses when every sample
// has the same outcome: there is nothing to separate.
func Train(samples []Sample) (*Predictor, error) {
	pos, neg := 0, 0
	for _, s := range samples {
		if s.Passed {
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		return nil, fmt.Errorf("%w (have %d passing, %d failing)", ErrOneClass, pos, neg)
	}
	p := &Predictor{Names: FeatureNames, Samples: len(samples), Positives: pos, Negatives: neg, When: time.Now()}
	p.Baseline = math.Max(float64(pos), float64(neg)) / float64(len(samples))
	p.fit(samples)
	// Cross-validate with the same recipe on folds.
	folds := 5
	if len(samples) < folds {
		folds = len(samples)
	}
	correct := 0
	for k := 0; k < folds; k++ {
		var train, test []Sample
		for i, s := range samples {
			if i%folds == k {
				test = append(test, s)
			} else {
				train = append(train, s)
			}
		}
		q := &Predictor{Names: FeatureNames}
		if !q.fit(train) {
			continue
		}
		for _, s := range test {
			if (q.Score(s.Features) >= 0.5) == s.Passed {
				correct++
			}
		}
	}
	p.Accuracy = float64(correct) / float64(len(samples))
	return p, nil
}

func (p *Predictor) vector(f map[string]float64) []float64 {
	x := make([]float64, len(p.Names))
	for i, n := range p.Names {
		x[i] = f[n]
		if len(p.Std) == len(p.Names) && p.Std[i] > 0 {
			x[i] = (x[i] - p.Mean[i]) / p.Std[i]
		}
	}
	return x
}

// fit runs gradient descent; false when the samples are one class.
func (p *Predictor) fit(samples []Sample) bool {
	n := len(p.Names)
	pos := 0
	for _, s := range samples {
		if s.Passed {
			pos++
		}
	}
	if pos == 0 || pos == len(samples) {
		return false
	}
	p.Mean, p.Std = make([]float64, n), make([]float64, n)
	for _, s := range samples {
		for i, name := range p.Names {
			p.Mean[i] += s.Features[name]
		}
	}
	for i := range p.Mean {
		p.Mean[i] /= float64(len(samples))
	}
	for _, s := range samples {
		for i, name := range p.Names {
			d := s.Features[name] - p.Mean[i]
			p.Std[i] += d * d
		}
	}
	for i := range p.Std {
		p.Std[i] = math.Sqrt(p.Std[i] / float64(len(samples)))
	}
	xs := make([][]float64, len(samples))
	for i, s := range samples {
		xs[i] = p.vector(s.Features)
	}
	p.Weight, p.Bias = make([]float64, n), 0
	for it := 0; it < trainIters; it++ {
		grad := make([]float64, n)
		gb := 0.0
		for i, s := range samples {
			y := 0.0
			if s.Passed {
				y = 1
			}
			err := sigmoid(dot(p.Weight, xs[i])+p.Bias) - y
			for j := range grad {
				grad[j] += err * xs[i][j]
			}
			gb += err
		}
		m := float64(len(samples))
		for j := range p.Weight {
			p.Weight[j] -= trainRate * (grad[j]/m + trainL2*p.Weight[j])
		}
		p.Bias -= trainRate * gb / m
	}
	return true
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Score is the predicted probability that the turn ends in success.
func (p *Predictor) Score(f map[string]float64) float64 {
	if p == nil || len(p.Weight) != len(p.Names) {
		return math.NaN()
	}
	return sigmoid(dot(p.Weight, p.vector(f)) + p.Bias)
}

// Explain names the features pulling the score down most, with their
// raw values, for a note the model can act on.
func (p *Predictor) Explain(f map[string]float64, n int) string {
	type term struct {
		name string
		z    float64
	}
	x := p.vector(f)
	var terms []term
	for i, name := range p.Names {
		if c := p.Weight[i] * x[i]; c < 0 {
			terms = append(terms, term{name, c})
		}
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].z < terms[j].z })
	var parts []string
	for i := 0; i < len(terms) && i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s=%s", terms[i].name, strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", f[terms[i].name]), "00"), ".")))
	}
	return strings.Join(parts, ", ")
}

// Save writes the predictor as JSON.
func (p *Predictor) Save(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadPredictor reads a saved predictor; a missing file yields nil, nil.
func LoadPredictor(path string) (*Predictor, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Predictor
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(p.Weight) != len(p.Names) || len(p.Mean) != len(p.Names) || len(p.Std) != len(p.Names) {
		return nil, fmt.Errorf("%s: malformed predictor", path)
	}
	return &p, nil
}
