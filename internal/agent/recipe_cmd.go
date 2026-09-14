package agent

import (
	"errors"
	"fmt"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// RecipeSave implements /recipe save <name> [--turn n] [--desc text]
// [k=v...] for every front-end: it crystallises one turn's tool calls
// into a saved recipe file under cwd and says where it went. --desc takes
// the rest of the line, so it is cut out before the remaining arguments
// are parsed. sessionID, when not empty, is recorded as the recipe's
// origin.
func (a *Agent) RecipeSave(sessionID, cwd, rest string) (cmdresult.Result, error) {
	rest, desc := recipe.CutDesc(rest)
	name, turn, params, err := recipe.ParseSaveArgs(rest)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if a.Log == nil {
		return cmdresult.Result{}, errors.New("no session log")
	}
	events := a.Log.Events()
	if turn == 0 {
		turn = recipe.LatestToolTurn(events)
	}
	// RecipeSteps, not recipe.FromEvents directly: it converts any
	// positional edit_file or write_file call into a content-anchored
	// step using this session's own change provenance, so a saved recipe
	// never carries a mutation whose replay could silently land on the
	// wrong lines of a file that moved on. See its doc comment.
	steps, err := a.RecipeSteps(turn)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("recipe save: %w", err)
	}
	steps, ps := recipe.Parameterise(steps, params)
	if desc == "" {
		desc = recipe.TurnSummary(events, turn)
	}
	r := recipe.Recipe{Name: name, Description: desc, Params: ps, Steps: steps}
	r.From.Session = sessionID
	r.From.Turn = turn
	path, err := recipe.Save(cwd, r)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("recipe save: %w", err)
	}
	return cmdresult.Result{Notes: []string{fmt.Sprintf("%s · turn %d · run it with /recipe run %s", textfmt.RelTo(cwd, path), turn, name)}}, nil
}

// RecipeOutcome is what every front-end shows when a /recipe run ends:
// the run's report, then one of four outcomes. A step that failed hands
// over to the model (Result.Handover, see RecipeHandover), as does a run
// whose steps all ran but whose project checks failed. A failure the
// harness itself denied (failed.Denied — a permission rule, a "no" from
// the user, a failed precondition) is never handed over: the model's own
// retry of the same call would be refused the same way, so it is
// reported instead.
func RecipeOutcome(r recipe.Recipe, report string, failed *RecipeFailure, err error) cmdresult.Result {
	res := cmdresult.Result{Sheet: report, SheetTitle: "recipe " + r.Name}
	switch {
	case err != nil:
		res.Error = fmt.Sprintf("recipe %s: %v", r.Name, err)
		res.Status = "error"
	case failed != nil && failed.Denied:
		what := fmt.Sprintf("step %d (%s)", failed.Step, failed.Tool)
		if failed.Verify {
			what = "the project's checks"
		}
		res.Error = fmt.Sprintf("recipe %s: %s was denied, not handed to the model: %s", r.Name, what, failed.Output)
		res.Status = "denied"
	case failed != nil && failed.Verify:
		res.Notes = []string{fmt.Sprintf("recipe %s: steps all ran, but the project's checks failed; handing over to the model", r.Name)}
		res.Handover = RecipeHandover(r, *failed)
	case failed != nil:
		res.Notes = []string{fmt.Sprintf("recipe %s stopped at step %d (%s); handing over to the model", r.Name, failed.Step, failed.Tool)}
		res.Handover = RecipeHandover(r, *failed)
	default:
		res.Status = "recipe " + r.Name + " done"
	}
	return res
}
