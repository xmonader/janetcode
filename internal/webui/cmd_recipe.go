package webui

import (
	"strings"

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
		rs, err := recipe.List(s.cwd())
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
	r, err := recipe.Load(s.cwd(), name)
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
// [k=v...] through Agent.RecipeSave.
func (s *server) saveRecipe(rest string) {
	sessionID := ""
	if s.sess() != nil {
		sessionID = s.sess().Meta.ID
	}
	s.showResult(s.opts.Agent.RecipeSave(sessionID, s.cwd(), rest))
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
	r, err := recipe.Load(s.cwd(), name)
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
	if g := s.opts.TrustGate; g != nil && !g.Exec(s.cwd(), false) {
		s.errf("recipe run: this project's recipes run once you trust it: /trust full (a dry run still works)")
		return
	}
	ctx, ok := s.begin("running recipe " + name)
	if !ok {
		s.errf("/recipe run waits for the turn to finish (esc cancels it)")
		return
	}
	s.emitStatus()
	ag := s.opts.Agent
	go func() {
		report, failed, err := ag.RunRecipe(ctx, r, args, s.apply)
		s.finishRecipe(r, report, failed, err)
	}()
}

// finishRecipe ends a background /recipe run: it shows the run's outcome
// (see agent.RecipeOutcome) and, when that hands over to the model,
// starts the handover through s.startTurn — the same path a goal round
// uses to start its next turn.
func (s *server) finishRecipe(r recipe.Recipe, report string, failed *agent.RecipeFailure, err error) {
	res := agent.RecipeOutcome(r, report, failed, err)
	if res.Handover != "" {
		// The handover goes first; what the user queued meanwhile
		// follows it when that turn ends.
		s.idle()
		s.showResult(res, nil)
		s.startTurn(res.Handover)
		return
	}
	// Otherwise the queue moves on as it does after a turn: idle alone
	// left a prompt sent during the run waiting until some later turn
	// ended, then ran it out of order. A run that failed drops it, as a
	// failed turn does.
	next, nextCtx, _ := s.end(err != nil)
	next, nextCtx = s.applyQueuedSettings(next, nextCtx)
	s.showResult(res, nil)
	s.emitStatus()
	if next != "" {
		s.runTurn(nextCtx, next)
	}
}
