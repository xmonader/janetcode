package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/xmonader/janetcode/internal/critic"
	"github.com/xmonader/janetcode/internal/session"
)

// Trajectory features: a fixed, ordered set of numbers describing how a
// turn has gone so far, computed from what the harness observed (tool
// calls, errors, edits, test runs, loops, tokens). Eval runs record them
// with the pass/fail outcome, a logistic model is trained on those, and
// at runtime the same features score the live turn.

var testCommandRe = regexp.MustCompile(`(?i)\b(test|pytest|spec|check|vet|lint|tsc|cargo build|go build|make)\b`)

// FoldTrajectory computes critic.FeatureNames from a session event log.
func FoldTrajectory(events []session.Event) map[string]float64 {
	f := map[string]float64{}
	for _, n := range critic.FeatureNames {
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
		if ev := turnEvents(a.logEvents(), a.turnN); len(ev) > 0 {
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
		case "write_file", "edit_file", "notebook_edit", "ast_edit":
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
	for _, n := range critic.FeatureNames {
		f[n] += 0 // every name present
	}
	return f
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
