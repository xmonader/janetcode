package main

// decisions_replay.go is Phase 0 of the Jev integration
// (docs/notes/jev-integration-spec.md): the offline calibration
// driver. `--decisions-replay <corpus.jsonl>` scores a labeled state
// corpus live — each line is {"name","truth","state"} — using the same
// question set and action mapping a real turn would take
// (agent.TriageQuestions / agent.TriageAction), writes
// <corpus>.answers.jsonl beside it, and prints the calibration table.
//
// The gate from the spec, restated: on a corpus whose truth is known by
// construction, the mapping must skip almost never on patch_caused
// rows, skip mostly on environmental/flaky/unrelated rows, and order
// the classes monotonically: mean caused probability of patch_caused
// above the others'. ≥70% overall correct is the spec's threshold; a
// wrong skip on patch_caused is the only costly mistake.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
)

type labeledState struct {
	Name  string `json:"name"`
	Truth string `json:"truth"`
	State string `json:"state"`
	File  string `json:"file,omitempty"`
	Seq   int    `json:"seq,omitempty"`
	// Consumer names which consumer's questions the row is labeled for.
	// Empty is the triage corpus, the only one there was when the flag
	// was written. A corpus says so in its rows rather than in a second
	// flag: the file and the question set belong together, and a corpus
	// scored with the wrong questions is worse than one refused.
	Consumer string `json:"consumer,omitempty"`
}

// decisionsReplayMode wires the flag: a decisions client built the same
// way a session builds one (config's decisions block, else the
// OpenRouter defaults), then the calibration.
func decisionsReplayMode(ctx context.Context, path string) error {
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return fmt.Errorf("decisions-replay: %w", err)
	}
	var client *jev.Client
	if cfg.Decisions != nil {
		key := os.Getenv(cfg.Decisions.KeyEnv())
		if key == "" {
			return fmt.Errorf("decisions-replay: %s is empty", cfg.Decisions.KeyEnv())
		}
		client = &jev.Client{BaseURL: cfg.Decisions.BaseURL, Key: key, NoBreaker: true,
			Model: cfg.Decisions.Model, Timeout: time.Duration(cfg.Decisions.TimeoutMs) * time.Millisecond}
	} else {
		// No block configured: default to the verified route rather than
		// refusing, because --decisions-replay is itself the thing being
		// tried out.
		key := os.Getenv("OPENROUTER_API_KEY")
		if key == "" {
			return fmt.Errorf("decisions-replay: no OPENROUTER_API_KEY and no decisions block in the config")
		}
		client = &jev.Client{Key: key, Model: "~typesafe/jev-latest", Timeout: jev.MaxTimeout, NoBreaker: true}
	}
	return runDecisionsReplay(ctx, path, client)
}

// Retries live here, in the replay driver, and nowhere else: the live
// gate deliberately never retries (a retried decision is a late
// decision), but calibration has the opposite trade-off — a 429 that
// becomes an error row is a row that silently does not count, which
// deflates the accuracy the spec gate is read from.
const (
	replayMaxAttempts = 4
	replayBackoffBase = 500 * time.Millisecond
	replayBackoffMax  = 8 * time.Second
	replayCallTimeout = 20 * time.Second
)

// evaluateRetrying answers one labeled state, retrying a rate limit or
// a 5xx with capped exponential backoff and honouring Retry-After when
// the server sends one. Anything else — a bad request, a malformed
// body, the circuit opening — comes back on the first attempt. sleep is
// injected so the tests do not wait.
func evaluateRetrying(ctx context.Context, j *jev.Client, state string, qs []jev.Question,
	sleep func(context.Context, time.Duration) error) (*jev.Result, error) {
	for attempt := 1; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, replayCallTimeout)
		res, err := j.Evaluate(callCtx, state, qs)
		cancel()
		if err == nil {
			return res, nil
		}
		var se *jev.StatusError
		if !errors.As(err, &se) || !se.Retryable() || attempt >= replayMaxAttempts {
			return nil, err
		}
		// The client's own circuit opens after three consecutive
		// errors and then refuses locally, so a further attempt would
		// only report the circuit instead of the failure that opened
		// it. Stop on the real error.
		if !j.Available() {
			return nil, err
		}
		if serr := sleep(ctx, replayBackoff(se.RetryAfter, attempt)); serr != nil {
			return nil, serr
		}
	}
}

// replayBackoff prefers the server's own Retry-After and otherwise
// doubles from the base, capped either way.
func replayBackoff(retryAfter time.Duration, attempt int) time.Duration {
	d := retryAfter
	if d <= 0 {
		d = replayBackoffBase << (attempt - 1)
	}
	if d > replayBackoffMax {
		d = replayBackoffMax
	}
	return d
}

// sleepCtx waits, or returns as soon as the run is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type replayRow struct {
	Name        string  `json:"name"`
	Truth       string  `json:"truth"`
	Class       string  `json:"class"`
	Caused      float64 `json:"caused"`
	Confidence  float64 `json:"confidence"`
	WorthRepair float64 `json:"worth_repair"`
	Model       string  `json:"model,omitempty"`
	LatencyMs   int64   `json:"latency_ms"`
	Skipped     bool    `json:"skipped"`
	Correct     bool    `json:"correct"`
}

type classTally struct {
	n, skipped, correct, costly int
	causedSum, confSum          float64
}

// truthWantsSkip: a wrong skip is consequential only where the truth
// would have been repaired — patch_caused, inconclusive.
func truthWantsSkip(truth string) bool {
	return truth != "patch_caused" && truth != "inconclusive"
}

func runDecisionsReplay(ctx context.Context, path string, j *jev.Client) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cases []labeledState
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c labeledState
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return fmt.Errorf("%s: no labeled states", path)
	}
	switch cases[0].Consumer {
	case "intake":
		return runIntakeReplay(ctx, path, j, cases)
	case "stuck":
		return runStuckReplay(ctx, path, j, cases)
	}
	questions := agent.TriageQuestions()
	tallies := map[string]*classTally{}
	var rows []string
	errorRows := 0
	for _, c := range cases {
		res, err := evaluateRetrying(ctx, j, c.State, questions, sleepCtx)
		t := tallies[c.Truth]
		if t == nil {
			t = &classTally{}
			tallies[c.Truth] = t
		}
		t.n++
		row := replayRow{Name: c.Name, Truth: c.Truth}
		if err != nil {
			errorRows++
			row.Model = "error: " + err.Error()
			rows = append(rows, mustJSONForReplay(row))
			fmt.Printf("%-38s %.40s  ERROR: %v\n", c.Name, c.Truth, err)
			continue
		}
		row.Class, row.Skipped = agent.TriageAction(res)
		row.Caused, _ = res.NoulAt("caused_by_change")
		row.WorthRepair, _ = res.NoulAt("worth_repair")
		_, conf, _ := res.ChoiceAt("failure_class")
		row.Confidence = conf
		row.Model = res.Model
		row.LatencyMs = res.Latency.Milliseconds()
		row.Correct = row.Skipped == truthWantsSkip(c.Truth)
		if row.Skipped && !truthWantsSkip(c.Truth) {
			t.costly++
		}
		t.skipped += btoi(row.Skipped)
		t.correct += btoi(row.Correct)
		t.causedSum += row.Caused
		t.confSum += row.Confidence
		rows = append(rows, mustJSONForReplay(row))
	}
	dst := strings.TrimSuffix(path, ".jsonl") + ".answers.jsonl"
	if err := os.WriteFile(dst, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	names := make([]string, 0, len(tallies))
	for k := range tallies {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Println("truth           n  skip% mean_p  mean_conf  correct  costly-skip")
	for _, k := range names {
		t := tallies[k]
		fmt.Printf("%-14s %2d %5d%% %7.2f %9.2f %8d %12d\n", k, t.n,
			100*t.skipped/maxInt(1, t.n), t.causedSum/n1(t.n),
			t.confSum/n1(t.n), t.correct, t.costly)
	}
	correctAll, costlyAll := 0, 0
	for _, t := range tallies {
		correctAll += t.correct
		costlyAll += t.costly
	}
	pct := 100 * correctAll / maxInt(1, len(cases))
	fmt.Printf("overall: %d/%d correct (%d%%), %d costly wrong skips (spec gate: >=70%% correct, no costly skip on patch_caused)\n",
		correctAll, len(cases), pct, costlyAll)
	// An error row counts as incorrect in that denominator, so a run
	// that lost rows to the service must not read as a run the mapping
	// did badly on.
	if errorRows > 0 {
		fmt.Printf("degraded: %d/%d rows ended in error after %d attempts each and count as incorrect above\n",
			errorRows, len(cases), replayMaxAttempts)
	}
	fmt.Println("rows: " + dst)
	return nil
}

// intakeRow is one scored intake row: what the classifier called the
// prompt, how sure it was, and the other two answers, which have no
// truth to score against and are recorded for their distribution.
type intakeRow struct {
	Name       string  `json:"name"`
	Truth      string  `json:"truth"`
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Complexity float64 `json:"complexity"`
	NeedsPlan  float64 `json:"needs_plan"`
	Model      string  `json:"model,omitempty"`
	LatencyMs  int64   `json:"latency_ms"`
	Correct    bool    `json:"correct"`
}

type intakeTally struct {
	n, correct                      int
	confSum, complexitySum, planSum float64
}

// runIntakeReplay scores the Phase 3a intake corpus: the only answer
// with a truth is kind, so that is what accuracy means here. complexity
// and needs_plan are reported per labeled kind because the thing worth
// knowing before anything acts on them is whether they separate at all
// — a needs_plan that reads the same for "explain this function" and
// "port the storage layer" is a question not worth asking.
func runIntakeReplay(ctx context.Context, path string, j *jev.Client, cases []labeledState) error {
	questions := agent.IntakeQuestions()
	tallies := map[string]*intakeTally{}
	var rows []string
	errorRows := 0
	for _, c := range cases {
		res, err := evaluateRetrying(ctx, j, agent.IntakeState(c.State), questions, sleepCtx)
		t := tallies[c.Truth]
		if t == nil {
			t = &intakeTally{}
			tallies[c.Truth] = t
		}
		t.n++
		row := intakeRow{Name: c.Name, Truth: c.Truth}
		if err != nil {
			errorRows++
			row.Model = "error: " + err.Error()
			rows = append(rows, mustJSONForReplay(row))
			fmt.Printf("%-38s %-12s ERROR: %v\n", c.Name, c.Truth, err)
			continue
		}
		row.Kind, row.Confidence, _ = res.ChoiceAt("kind")
		row.Complexity, _, _, _ = res.ScoreAt("complexity")
		row.NeedsPlan, _ = res.NoulAt("needs_plan")
		row.Model, row.LatencyMs = res.Model, res.Latency.Milliseconds()
		row.Correct = row.Kind == c.Truth
		t.correct += btoi(row.Correct)
		t.confSum += row.Confidence
		t.complexitySum += row.Complexity
		t.planSum += row.NeedsPlan
		rows = append(rows, mustJSONForReplay(row))
	}
	dst := strings.TrimSuffix(path, ".jsonl") + ".answers.jsonl"
	if err := os.WriteFile(dst, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	names := make([]string, 0, len(tallies))
	for k := range tallies {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Println("truth         n  correct  mean_conf  mean_complexity  mean_needs_plan")
	correctAll := 0
	for _, k := range names {
		t := tallies[k]
		correctAll += t.correct
		fmt.Printf("%-12s %2d %8d %10.2f %16.2f %16.2f\n", k, t.n, t.correct,
			t.confSum/n1(t.n), t.complexitySum/n1(t.n), t.planSum/n1(t.n))
	}
	fmt.Printf("overall: %d/%d kind correct (%d%%)\n",
		correctAll, len(cases), 100*correctAll/maxInt(1, len(cases)))
	fmt.Println("phase 3a is log-only: these numbers are what a later gate would be read from, not a threshold anything acts on today")
	if errorRows > 0 {
		fmt.Printf("degraded: %d/%d rows ended in error after %d attempts each and count as incorrect above\n",
			errorRows, len(cases), replayMaxAttempts)
	}
	fmt.Println("rows: " + dst)
	return nil
}

func mustJSONForReplay(v any) string { b, _ := json.Marshal(v); return string(b) }
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func n1(n int) float64 {
	if n < 1 {
		return 1
	}
	return float64(n)
}
