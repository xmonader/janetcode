package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/recipe"
)

// recipeCommand implements /recipe [list | show <name> | save <name>
// [--turn n] [--desc text] [k=v...] | run <name> [--dry-run] [k=v...]].
// list/show/save read and write the recipe files and print; run is the
// only branch that starts anything, since it drives the agent through
// tool calls that need the same approval a turn would. The dispatcher
// already blocks /recipe while a turn is running (it is idle-only, see
// slash.idleOnly): save reads this session's own log, and run would
// otherwise race a turn already using the agent.
func (m *model) recipeCommand(arg string) tea.Cmd {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch verb {
	case "", "list":
		rs, err := recipe.List(m.opts.Cwd)
		if err != nil {
			m.transcript.errorf("recipe list: %v", err)
			break
		}
		m.transcript.info(recipe.FormatList(rs))
	case "show":
		m.showRecipe(strings.TrimSpace(rest))
	case "save":
		m.saveRecipe(rest)
	case "run":
		return m.runRecipe(rest)
	default:
		m.transcript.errorf("usage: /recipe [list | show <name> | save <name> [--turn n] [k=v...] | run <name> [--dry-run] [k=v...]]")
	}
	m.refresh(false)
	return nil
}

func (m *model) showRecipe(name string) {
	if name == "" {
		m.transcript.errorf("usage: /recipe show <name>")
		return
	}
	r, err := recipe.Load(m.opts.Cwd, name)
	if err != nil {
		m.transcript.errorf("recipe show: %v", err)
		return
	}
	steps, err := recipe.Expand(r, nil)
	if err != nil {
		m.transcript.info(recipe.Format(r, r.Steps) + "\n\n(shown as saved: " + err.Error() + ")")
		return
	}
	m.transcript.info(recipe.Format(r, steps))
}

// saveRecipe implements /recipe save <name> [--turn n] [--desc text]
// [k=v...] through Agent.RecipeSave: it crystallises one turn's tool
// calls into a saved recipe file.
func (m *model) saveRecipe(rest string) {
	sessionID := ""
	if m.opts.Session != nil {
		sessionID = m.opts.Session.Meta.ID
	}
	m.showResult(m.opts.Agent.RecipeSave(sessionID, m.opts.Cwd, rest))
}

// runRecipe implements /recipe run <name> [--dry-run] [k=v...]. A dry run
// only prints the expanded steps. A real run drives the agent through
// Agent.RunRecipe exactly like a turn would (approvals apply); on a
// failed step it hands the model the failure with agent.RecipeHandover,
// through the ordinary submit path, the same way a goal round starts its
// next turn.
func (m *model) runRecipe(rest string) tea.Cmd {
	name, args, dryRun, err := recipe.ParseRunArgs(rest)
	if err != nil {
		m.transcript.errorf("%v", err)
		return nil
	}
	r, err := recipe.Load(m.opts.Cwd, name)
	if err != nil {
		m.transcript.errorf("recipe run: %v", err)
		return nil
	}
	steps, err := recipe.Expand(r, args)
	if err != nil {
		m.transcript.errorf("recipe run: %v", err)
		return nil
	}
	if dryRun {
		m.transcript.info(recipe.FormatSteps(steps))
		return nil
	}
	m.startTurn("running recipe " + name)
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.refresh(false)
	go func() {
		report, failed, err := ag.RunRecipe(ctx, r, args, queue.push)
		send(recipeDoneMsg{recipe: r, report: report, failed: failed, err: err})
	}()
	return m.spinner.Tick
}

// recipeDoneMsg reports the end of a background /recipe run.
type recipeDoneMsg struct {
	recipe recipe.Recipe
	report string
	failed *agent.RecipeFailure
	err    error
}

// finishRecipe handles recipeDoneMsg: it shows the run's outcome (see
// agent.RecipeOutcome) and, when that hands over to the model, submits
// the handover — the same submit path a queued prompt or a goal round
// uses to start its next turn.
func (m *model) finishRecipe(msg recipeDoneMsg) tea.Cmd {
	cancelled := m.turn.cancelling
	m.turn.running = false
	m.turn.cancelling = false
	if m.turn.cancel != nil {
		m.turn.cancel()
		m.turn.cancel = nil
	}
	m.turn.setPhase("")
	res := agent.RecipeOutcome(msg.recipe, msg.report, msg.failed, msg.err)
	m.showResult(res, nil)
	m.term.turnEnded(res.Error == "" && res.Handover == "", m.status)
	m.refresh(false)
	if res.Handover != "" {
		// What was queued meanwhile waits behind the handover turn,
		// whose end sends it.
		return m.submit(res.Handover)
	}
	if cancelled || msg.err != nil {
		// As after a turn: what was queued behind a run that was
		// cancelled or failed goes back in the input, not to the model.
		m.restoreQueued()
		return nil
	}
	return m.sendQueued()
}

// sendQueued starts the first prompt queued while something ran that was
// not a turn (/verify, /recipe run), the way the end of a turn does.
// Without it a prompt queued "until this turn ends" waited for the end of
// some later turn instead.
func (m *model) sendQueued() tea.Cmd {
	next, ok := m.turn.dequeue()
	if !ok {
		return nil
	}
	return m.submit(next)
}
