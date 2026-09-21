package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/jev"
)

// stuckRow is one answered step-state of the mid-turn corpus. Each of
// the three questions is a probability, so the row keeps all three and
// the scoring happens afterwards over a threshold sweep — the point of
// this replay is to find out whether any threshold separates a wasted
// step from a healthy one at all, not to defend a threshold picked in
// advance.
type stuckRow struct {
	Name      string  `json:"name"`
	Truth     string  `json:"truth"`
	Stuck     float64 `json:"stuck"`
	OnGoal    float64 `json:"on_goal"`
	NextFail  float64 `json:"next_call_will_fail"`
	Model     string  `json:"model,omitempty"`
	LatencyMs int64   `json:"latency_ms"`
}

// stuckThresholds is the sweep. A mid-turn consumer that interrupts a
// healthy run is the costly error, so the interesting end is the high
// one: precision at 0.8 and 0.9 decides whether this is worth wiring.
var stuckThresholds = []float64{0.5, 0.6, 0.7, 0.8, 0.9}

// runStuckReplay scores the offline mid-turn corpus. Truth is "waste"
// (the step the session log shows was wasted: an errored call, a loop,
// or a step in the dead tail of a run that never recovered) or
// "healthy". Positive class is waste throughout.
func runStuckReplay(ctx context.Context, path string, j *jev.Client, cases []labeledState) error {
	questions := agent.StuckQuestions()
	var rows []stuckRow
	var lines []string
	errorRows := 0
	for _, c := range cases {
		res, err := evaluateRetrying(ctx, j, c.State, questions, sleepCtx)
		row := stuckRow{Name: c.Name, Truth: c.Truth}
		if err != nil {
			errorRows++
			row.Model = "error: " + err.Error()
			lines = append(lines, mustJSONForReplay(row))
			fmt.Printf("%-44s %-8s ERROR: %v\n", c.Name, c.Truth, err)
			continue
		}
		_, row.Stuck, row.OnGoal, row.NextFail = agent.StuckAction(res, agent.StuckThreshold)
		row.Model, row.LatencyMs = res.Model, res.Latency.Milliseconds()
		rows = append(rows, row)
		lines = append(lines, mustJSONForReplay(row))
	}
	dst := strings.TrimSuffix(path, ".jsonl") + ".answers.jsonl"
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}

	pos := 0
	for _, r := range rows {
		if r.Truth == "waste" {
			pos++
		}
	}
	fmt.Printf("%d scored rows (%d waste, %d healthy), %d errors\n", len(rows), pos, len(rows)-pos, errorRows)
	fmt.Println("question              thr    tp  fp  fn    precision  recall")
	type q struct {
		name string
		get  func(stuckRow) float64
		// invert is for on_goal, where the waste signal is a LOW
		// probability: an off-goal step is one the model says is not
		// serving the goal.
		invert bool
	}
	for _, qq := range []q{
		{name: "stuck", get: func(r stuckRow) float64 { return r.Stuck }},
		{name: "on_goal(inverted)", get: func(r stuckRow) float64 { return r.OnGoal }, invert: true},
		{name: "next_call_will_fail", get: func(r stuckRow) float64 { return r.NextFail }},
	} {
		for _, thr := range stuckThresholds {
			tp, fp, fn := 0, 0, 0
			for _, r := range rows {
				v := qq.get(r)
				flag := v >= thr
				if qq.invert {
					flag = v <= 1-thr
				}
				switch {
				case flag && r.Truth == "waste":
					tp++
				case flag:
					fp++
				case r.Truth == "waste":
					fn++
				}
			}
			prec, rec := 0.0, 0.0
			if tp+fp > 0 {
				prec = float64(tp) / float64(tp+fp)
			}
			if tp+fn > 0 {
				rec = float64(tp) / float64(tp+fn)
			}
			fmt.Printf("%-20s %.1f %5d %3d %3d %11.2f %7.2f\n", qq.name, thr, tp, fp, fn, prec, rec)
		}
	}
	fmt.Println("offline only: nothing mid-turn consults these answers today")
	if errorRows > 0 {
		return fmt.Errorf("%d rows errored", errorRows)
	}
	return nil
}
