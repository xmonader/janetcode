package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/jev"
)

// The agreement check for the offline annotation consumer
// (internal/agent/jevannotate.go). `--decisions-annotate` produces
// labels nothing can check; this scores the same question set against a
// corpus whose truth is by construction — the waste labels the stuck
// corpus carries, harvested from recorded eval runs — so the
// annotations can be given an agreement rate instead of a shrug.
//
// The corpus is the stuck corpus with its rows retagged
// `"consumer": "annotate"`; docs/notes/jev.md says how to make it and
// why it is not committed twice.

type annotateScoreRow struct {
	Name      string  `json:"name"`
	Truth     string  `json:"truth"`
	Wasted    float64 `json:"wasted"`
	Misuse    float64 `json:"tool_misuse"`
	Progress  float64 `json:"progress"`
	Outcome   string  `json:"outcome"`
	Flagged   bool    `json:"flagged"`
	Correct   bool    `json:"correct"`
	Model     string  `json:"model,omitempty"`
	LatencyMs int64   `json:"latency_ms"`
}

// runAnnotateReplay scores `wasted` against a waste/healthy truth. The
// numbers that matter are precision and recall on waste: an annotation
// that flags everything agrees with nothing.
func runAnnotateReplay(ctx context.Context, path string, j *jev.Client, cases []labeledState) error {
	questions := agent.AnnotateQuestions()
	var rows []string
	var tp, fp, fn, tn, errorRows int
	for _, c := range cases {
		res, err := evaluateRetrying(ctx, j, c.State, questions, sleepCtx)
		row := annotateScoreRow{Name: c.Name, Truth: c.Truth}
		if err != nil {
			errorRows++
			row.Model = "error: " + err.Error()
			rows = append(rows, mustJSONForReplay(row))
			fmt.Printf("%-44s %-8s ERROR: %v\n", c.Name, c.Truth, err)
			continue
		}
		a := agent.AnnotateOf(res)
		row.Wasted, row.Misuse, row.Progress = a.Wasted, a.Misuse, a.Progress
		row.Outcome, row.Model, row.LatencyMs = a.Outcome, a.Model, a.LatencyMs
		row.Flagged = a.Wasted >= agent.AnnotateWasteThreshold
		want := c.Truth == "waste"
		row.Correct = row.Flagged == want
		switch {
		case row.Flagged && want:
			tp++
		case row.Flagged:
			fp++
		case want:
			fn++
		default:
			tn++
		}
		rows = append(rows, mustJSONForReplay(row))
	}
	dst := strings.TrimSuffix(path, ".jsonl") + ".answers.jsonl"
	if err := os.WriteFile(dst, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	scored := tp + fp + fn + tn
	fmt.Printf("\nwasted >= %.1f against the by-construction waste labels, %d rows scored\n",
		agent.AnnotateWasteThreshold, scored)
	fmt.Printf("  tp %d  fp %d  fn %d  tn %d\n", tp, fp, fn, tn)
	fmt.Printf("  agreement %d/%d (%d%%)  precision %.2f  recall %.2f\n",
		tp+tn, scored, 100*(tp+tn)/maxInt(1, scored),
		float64(tp)/n1(tp+fp), float64(tp)/n1(tp+fn))
	if errorRows > 0 {
		fmt.Printf("degraded: %d rows ended in error after %d attempts each and are not scored\n", errorRows, replayMaxAttempts)
	}
	fmt.Println("rows: " + dst)
	return nil
}
