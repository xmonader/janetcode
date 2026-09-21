package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
)

// `--decisions-annotate <sessions>` labels recorded sessions offline:
// every turn of every session gets one decision call and one row. It is
// item 5 of the surviving list in docs/notes/jev.md and the only one
// with no live risk — the sessions are already over, and the worst a
// wrong answer does is put a wrong row in a training file.
//
// Two files come out. The annotations themselves are a JSONL of
// agent.Annotation rows. The second is shaped like an eval result file
// so `--eval-train` can read it without knowing where it came from:
// internal/critic fits on {Features, Passed}, the features are the same
// agent.FoldTrajectory vector an eval run records, and the label is the
// annotated outcome of the session's last turn. That is the whole point
// of the exercise — the critic has never had a negative class, and a
// recorded session that went badly is one.

// annotateOut is where the two files land, named after the input.
type annotateOut struct {
	rows  []string
	train eval.File
}

// decisionsAnnotateMode wires the flag: the same client the calibration
// replay builds (NoBreaker, retried, scrubbed at the wire), then a pass
// over every session file under path.
func decisionsAnnotateMode(ctx context.Context, path, outDir string) error {
	client, err := offlineDecisionsClient("decisions-annotate")
	if err != nil {
		return err
	}
	files, err := sessionFiles(path)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("decisions-annotate: no session files under %s", path)
	}
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return runAnnotate(ctx, files, outDir, client)
}

// sessionFiles is the input: one .jsonl session, or every .jsonl under a
// directory, oldest name first so a rerun writes the same order.
func sessionFiles(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(path, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// annotateOutcomePassed is the label the critic is fitted on: a session
// counts as a pass when its last annotated turn came back "success".
// partial, failed and abandoned are all negatives — the critic's
// question is "will this run pass", and three of the four ways a session
// can end are answers of "no".
func annotateOutcomePassed(last string) bool { return last == "success" }

func runAnnotate(ctx context.Context, files []string, outDir string, j *jev.Client) error {
	questions := agent.AnnotateQuestions()
	var out annotateOut
	out.train = eval.File{When: time.Now(), Runs: len(files), Variant: "decisions-annotate"}
	var turns, errs, wasteful, misused int
	outcomes := map[string]int{}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		sess, _, err := session.Open(filepath.Dir(f), id)
		if err != nil {
			fmt.Printf("%-24s SKIP: %v\n", id, err)
			continue
		}
		events := sess.Events()
		annTurns := agent.AnnotateTurns(events)
		if len(annTurns) == 0 {
			fmt.Printf("%-24s SKIP: no turns\n", id)
			continue
		}
		lastOutcome := ""
		for _, t := range annTurns {
			state := agent.AnnotateState(t.Prompt, t.Steps, t.Verdict)
			res, err := evaluateRetrying(ctx, j, state, questions, sleepCtx)
			row := agent.Annotation{Session: id, Turn: t.Turn, Calls: len(t.Steps),
				Failed: t.Failed, Verdict: t.Verdict}
			turns++
			if err != nil {
				errs++
				row.Err = err.Error()
				out.rows = append(out.rows, mustJSONForReplay(row))
				fmt.Printf("%-24s turn %2d  ERROR: %v\n", id, t.Turn, err)
				continue
			}
			a := agent.AnnotateOf(res)
			a.Session, a.Turn, a.Calls, a.Failed, a.Verdict = id, t.Turn, len(t.Steps), t.Failed, t.Verdict
			lastOutcome = a.Outcome
			outcomes[a.Outcome]++
			if a.Wasted >= agent.AnnotateWasteThreshold {
				wasteful++
			}
			if a.Misuse >= agent.AnnotateWasteThreshold {
				misused++
			}
			out.rows = append(out.rows, mustJSONForReplay(a))
		}
		if lastOutcome == "" {
			continue
		}
		// One training sample a session: the trajectory features the eval
		// harness would have recorded, labelled by how the session ended.
		out.train.Results = append(out.train.Results, eval.Result{
			Name:     id,
			Passed:   annotateOutcomePassed(lastOutcome),
			Features: agent.FoldTrajectory(events),
		})
		fmt.Printf("%-24s %2d turns, ended %s\n", id, len(annTurns), lastOutcome)
	}
	rowsPath := filepath.Join(outDir, "annotations.jsonl")
	if err := os.WriteFile(rowsPath, []byte(strings.Join(out.rows, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	trainPath := filepath.Join(outDir, "critic-train.json")
	blob, err := json.MarshalIndent(out.train, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(trainPath, blob, 0o644); err != nil {
		return err
	}
	pos := 0
	for _, r := range out.train.Results {
		if r.Passed {
			pos++
		}
	}
	fmt.Printf("\n%d turns annotated across %d sessions (%d errored)\n", turns, len(out.train.Results), errs)
	names := make([]string, 0, len(outcomes))
	for k := range outcomes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Printf("  outcome %-10s %d\n", k, outcomes[k])
	}
	fmt.Printf("  wasted >= %.1f: %d turns; tool_misuse >= %.1f: %d turns\n",
		agent.AnnotateWasteThreshold, wasteful, agent.AnnotateWasteThreshold, misused)
	fmt.Printf("training file: %d samples, %d passing, %d failing -> %s\n",
		len(out.train.Results), pos, len(out.train.Results)-pos, trainPath)
	if pos == 0 || pos == len(out.train.Results) {
		fmt.Println("one class only: --eval-train will refuse this, as it refuses any single-class set")
	}
	fmt.Println("rows: " + rowsPath)
	fmt.Println("these are model annotations, not ground truth; the agreement check against the by-construction waste labels is in docs/notes/jev.md")
	return nil
}
