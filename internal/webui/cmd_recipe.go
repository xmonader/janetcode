package webui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/recipe"
)

// recipeCommand implements /recipe [list | show <name> | save <name>
// [--turn n] [--desc text] [k=v...] | run <name> [--dry-run] [k=v...]].
// list/show/save read and write the recipe files and print; run drives
// the agent through tool calls that need the same approval a turn would,
// so it is the only branch that starts anything. The dispatcher already
// blocks /recipe while a turn is running (it is idle-only).
func (s *server) recipeCommand(arg string) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch verb {
	case "", "list":
		rs, err := recipe.List(s.opts.Cwd)
		if err != nil {
			s.errf("recipe list: %v", err)
			return
		}
		s.sheet("recipes", recipe.FormatList(rs))
	case "show":
		s.showRecipe(strings.TrimSpace(rest))
	case "save":
		s.saveRecipe(rest)
	case "run":
		s.runRecipe(rest)
	default:
		s.errf("usage: /recipe [list | show <name> | save <name> [--turn n] [k=v...] | run <name> [--dry-run] [k=v...]]")
	}
}

func (s *server) showRecipe(name string) {
	if name == "" {
		s.errf("usage: /recipe show <name>")
		return
	}
	r, err := recipe.Load(s.opts.Cwd, name)
	if err != nil {
		s.errf("recipe show: %v", err)
		return
	}
	steps, err := recipe.Expand(r, nil)
	if err != nil {
		s.sheet("recipe "+name, recipe.Format(r, r.Steps)+"\n\n(shown as saved: "+err.Error()+")")
		return
	}
	s.sheet("recipe "+name, recipe.Format(r, steps))
}

// saveRecipe implements /recipe save <name> [--turn n] [--desc text]
// [k=v...]. --desc, when given, takes the rest of the line, so it is cut
// out before the simpler k=v tokenising recipe.ParseSaveArgs does.
func (s *server) saveRecipe(rest string) {
	rest, desc := recipe.CutDesc(rest)
	name, turn, params, err := recipe.ParseSaveArgs(rest)
	if err != nil {
		s.errf("%v", err)
		return
	}
	ag := s.opts.Agent
	if ag.Log == nil {
		s.errf("no session log")
		return
	}
	events := ag.Log.Events()
	if turn == 0 {
		turn = recipe.LatestToolTurn(events)
	}
	steps, err := recipe.FromEvents(events, turn)
	if err != nil {
		s.errf("recipe save: %v", err)
		return
	}
	steps, ps := recipe.Parameterise(steps, params)
	if desc == "" {
		desc = recipe.TurnSummary(events, turn)
	}
	r := recipe.Recipe{Name: name, Description: desc, Params: ps, Steps: steps}
	if s.opts.Session != nil {
		r.From.Session = s.opts.Session.Meta.ID
	}
	r.From.Turn = turn
	path, err := recipe.Save(s.opts.Cwd, r)
	if err != nil {
		s.errf("recipe save: %v", err)
		return
	}
	s.notice(fmt.Sprintf("%s · turn %d · run it with /recipe run %s", path, turn, name))
}

// runRecipe implements /recipe run <name> [--dry-run] [k=v...]. A dry run
// only prints the expanded steps. A real run drives the agent through
// Agent.RunRecipe exactly like a turn would (approvals apply); on a
// failed step it hands over to the model with agent.RecipeHandover, the
// same way a goal round starts its next turn with s.startTurn.
func (s *server) runRecipe(rest string) {
	name, args, dryRun, err := recipe.ParseRunArgs(rest)
	if err != nil {
		s.errf("%v", err)
		return
	}
	r, err := recipe.Load(s.opts.Cwd, name)
	if err != nil {
		s.errf("recipe run: %v", err)
		return
	}
	steps, err := recipe.Expand(r, args)
	if err != nil {
		s.errf("recipe run: %v", err)
		return
	}
	if dryRun {
		s.sheet("recipe "+name, recipe.FormatSteps(steps))
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.errf("/recipe run waits for the turn to finish (esc cancels it)")
		return
	}
	s.running = true
	s.phase = "running recipe " + name
	s.started = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	s.emitStatus()
	ag := s.opts.Agent
	go func() {
		report, failed, err := ag.RunRecipe(ctx, r, args, s.apply)
		s.finishRecipe(r, report, failed, err)
	}()
}

// finishRecipe ends a background /recipe run: it prints the run's report
// and, on a failed step, hands over to the model with
// agent.RecipeHandover through s.startTurn — the same path a goal round
// uses to start its next turn.
func (s *server) finishRecipe(r recipe.Recipe, report string, failed *agent.RecipeFailure, err error) {
	s.mu.Lock()
	s.running = false
	s.phase = ""
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
	if report != "" {
		s.upsert(&entry{Kind: "sheet", Name: "recipe " + r.Name, Text: report})
	}
	switch {
	case err != nil:
		s.upsert(&entry{Kind: "error", Text: "recipe " + r.Name + ": " + err.Error(), Error: true})
		s.statusSet("error")
	case failed != nil && failed.Verify:
		s.notice(fmt.Sprintf("recipe %s: steps all ran, but the project's checks failed; handing over to the model", r.Name))
		s.startTurn(agent.RecipeHandover(r, *failed))
		return
	case failed != nil:
		s.notice(fmt.Sprintf("recipe %s stopped at step %d (%s); handing over to the model", r.Name, failed.Step, failed.Tool))
		s.startTurn(agent.RecipeHandover(r, *failed))
		return
	default:
		s.statusSet("recipe " + r.Name + " done")
	}
	s.emitStatus()
}
