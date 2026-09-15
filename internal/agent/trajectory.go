package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// Trajectory features: a fixed, ordered set of numbers describing how a
// turn has gone so far, computed from what the harness observed (tool
// calls, errors, edits, test runs, loops, tokens). Eval runs record them
// with the pass/fail outcome, a logistic model is trained on those, and
// at runtime the same features score the live turn.

// FeatureNames is the feature order; Trajectory fills every one.
var FeatureNames = []string{
	"tool_calls", "tool_errors", "error_rate", "model_calls", "reads", "edits",
	"files_edited", "tests", "test_failures", "edits_since_test", "max_repeat",
	"cycle_rounds", "input_ktokens", "output_ktokens", "compactions", "minutes",
}

var testCommandRe = regexp.MustCompile(`(?i)\b(test|pytest|spec|check|vet|lint|tsc|cargo build|go build|make)\b`)

// FoldTrajectory computes FeatureNames from a session event log.
func FoldTrajectory(events []session.Event) map[string]float64 {
	f := map[string]float64{}
	for _, n := range FeatureNames {
		f[n] = 0
	}
	var calls, errs, reads, edits, tests, testFails, compact, model int
	inTok, outTok := 0, 0
	files := map[string]bool{}
	repeat := map[string]int{}
	var seq []signature
	var start, end time.Time
	editsSince := 0.0
	for _, e := range events {
		if start.IsZero() && !e.Time.IsZero() {
			start = e.Time
		}
		if !e.Time.IsZero() {
			end = e.Time
		}
		switch e.Type {
		case session.TypeStepStart:
			model++
		case session.TypeAssistant:
			if e.Usage != nil {
				inTok += e.Usage.InputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
				outTok += e.Usage.OutputTokens
			}
		case session.TypeCompactStart, session.TypeCompactSummary:
			if e.Type == session.TypeCompactStart {
				compact++
			}
		case session.TypeToolCall:
			calls++
			repeat[e.Name+" "+e.Arguments]++
			if e.Name == "read_file" {
				reads++
			}
		case session.TypeToolResult:
			if e.CallID == "" {
				continue // the surface tool-result message, not one call
			}
			if e.IsError {
				errs++
			}
			// Counted on the result, and only when it worked, so the
			// features mean the same thing as the ones trained on.
			switch {
			case editTool(e.Name):
				if !e.IsError {
					edits++
					editsSince++
					if p := argPath(e.Arguments); p != "" {
						files[p] = true
					}
				}
			case e.Name == "bash" && testCommandRe.MatchString(e.Arguments):
				tests++
				editsSince = 0
				if e.IsError {
					testFails++
				}
			}
			seq = append(seq, newSignature(e.Name, []byte(e.Arguments), e.IsError, false))
		}
	}
	f["tool_calls"] = float64(calls)
	f["tool_errors"] = float64(errs)
	if calls > 0 {
		f["error_rate"] = float64(errs) / float64(calls)
	}
	f["model_calls"] = float64(model)
	f["reads"] = float64(reads)
	f["edits"] = float64(edits)
	f["files_edited"] = float64(len(files))
	f["tests"] = float64(tests)
	f["test_failures"] = float64(testFails)
	f["edits_since_test"] = editsSince
	maxR := 0
	for _, n := range repeat {
		if n > maxR {
			maxR = n
		}
	}
	f["max_repeat"] = float64(maxR)
	d := loopDetector{seq: seq}
	if _, reps := d.cycle(); reps > 0 {
		f["cycle_rounds"] = float64(reps)
	}
	f["input_ktokens"] = float64(inTok) / 1000
	f["output_ktokens"] = float64(outTok) / 1000
	f["compactions"] = float64(compact)
	if !start.IsZero() && !end.IsZero() {
		f["minutes"] = end.Sub(start).Minutes()
	}
	return f
}

// argPath is the file a tool call names, for counting distinct files.
func argPath(arguments string) string {
	var in struct {
		Path string `json:"path"`
		File string `json:"file"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	if in.Path != "" {
		return in.Path
	}
	return in.File
}

// turnEvents is the slice of the log belonging to the running turn. The
// features describe one turn: folding the whole log of a resumed session
// would hand the critic totals from earlier runs, which no training sample
// ever contained.
func turnEvents(events []session.Event, turn int) []session.Event {
	if turn <= 0 {
		return events
	}
	out := make([]session.Event, 0, len(events))
	for _, e := range events {
		if e.Turn == turn {
			out = append(out, e)
		}
	}
	return out
}

// Trajectory describes the running turn as feature values.
func (a *Agent) Trajectory() map[string]float64 {
	if a.Log != nil {
		if ev := turnEvents(a.Log.Events(), a.turnN); len(ev) > 0 {
			f := FoldTrajectory(ev)
			if !a.turnStart.IsZero() {
				f["minutes"] = time.Since(a.turnStart).Minutes()
			}
			return f
		}
	}
	st := a.Stats()
	calls, errs := st.ToolCalls()
	f := map[string]float64{}
	f["tool_calls"] = float64(calls)
	f["tool_errors"] = float64(errs)
	if calls > 0 {
		f["error_rate"] = float64(errs) / float64(calls)
	}
	f["model_calls"] = float64(st.ModelCalls)
	for _, t := range st.Tools {
		switch t.Name {
		case "read_file":
			f["reads"] += float64(t.Calls)
		case "write_file", "edit_file":
			f["edits"] += float64(t.Calls - t.Errors)
		}
	}
	a.cp.mu.Lock()
	files := map[string]bool{}
	for _, c := range a.cp.changes {
		files[c.Path] = true
	}
	a.cp.mu.Unlock()
	f["files_edited"] = float64(len(files))
	a.loopMu.Lock()
	edits := 0.0
	for _, s := range a.loops.seq {
		switch {
		case s.name == "bash" && testCommandRe.MatchString(s.subject):
			f["tests"]++
			if s.failed {
				f["test_failures"]++
			}
			edits = 0
		case editTool(s.name):
			if !s.failed {
				edits++
			}
		}
	}
	f["edits_since_test"] = edits
	for _, n := range a.loops.calls {
		f["max_repeat"] = math.Max(f["max_repeat"], float64(n))
	}
	if _, reps := a.loops.cycle(); reps > 0 {
		f["cycle_rounds"] = float64(reps)
	}
	a.loopMu.Unlock()
	f["input_ktokens"] = float64(a.Usage.InputTokens+a.Usage.CacheReadTokens+a.Usage.CacheWriteTokens) / 1000
	f["output_ktokens"] = float64(a.Usage.OutputTokens) / 1000
	f["compactions"] = float64(st.Compactions)
	if !a.turnStart.IsZero() {
		f["minutes"] = time.Since(a.turnStart).Minutes()
	}
	for _, n := range FeatureNames {
		f[n] += 0 // every name present
	}
	return f
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

const (
	// predictorEvery tool calls the live turn is scored; below
	// predictorWarnAt the model gets one note per turn.
	predictorEvery  = 5
	predictorWarnAt = 0.35
)

// critique scores the running turn and returns a note for the model the
// first time the score falls under the threshold.
func (a *Agent) critique(emit func(Event)) string {
	if a.Predictor == nil || a.Off["critic"] {
		return ""
	}
	calls, _ := a.Stats().ToolCalls()
	if calls == 0 || calls%predictorEvery != 0 {
		return ""
	}
	f := a.Trajectory()
	p := a.Predictor.Score(f)
	if math.IsNaN(p) {
		return ""
	}
	a.lastScore = p
	if p >= predictorWarnAt || a.warned {
		return ""
	}
	a.warned = true
	why := a.Predictor.Explain(f, 3)
	msg := fmt.Sprintf("trajectory critic: turns that looked like this succeeded %.0f%% of the time in the eval history (%s)", p*100, why)
	emit(Event{Kind: EvNotice, Text: msg})
	a.logEvent(session.Event{Type: session.TypeCritic, Output: msg, Extra: mustJSON(f)})
	return "\n\n[harness] " + msg + ". Step back: check what the user actually asked for, verify the current state before more edits, and say what is blocking you if something is."
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
