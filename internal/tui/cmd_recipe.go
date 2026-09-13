package tui

import (
	"context"
	"fmt"
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
// [k=v...]: it crystallises one turn's tool calls (see recipe.FromEvents)
// into a saved file. --desc, when given, takes the rest of the line, so
// it is cut out before the remaining arguments are parsed.
func (m *model) saveRecipe(rest string) {
	rest, desc := recipe.CutDesc(rest)
	name, turn, params, err := recipe.ParseSaveArgs(rest)
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	ag := m.opts.Agent
	if ag.Log == nil {
		m.transcript.errorf("no session log")
		return
	}
	events := ag.Log.Events()
	if turn == 0 {
		turn = recipe.LatestToolTurn(events)
	}
	steps, err := recipe.FromEvents(events, turn)
	if err != nil {
		m.transcript.errorf("recipe save: %v", err)
		return
	}
	steps, ps := recipe.Parameterise(steps, params)
	if desc == "" {
		desc = recipe.TurnSummary(events, turn)
	}
	r := recipe.Recipe{Name: name, Description: desc, Params: ps, Steps: steps}
	if m.opts.Session != nil {
		r.From.Session = m.opts.Session.Meta.ID
	}
	r.From.Turn = turn
	path, err := recipe.Save(m.opts.Cwd, r)
	if err != nil {
		m.transcript.errorf("recipe save: %v", err)
		return
	}
	m.note(shortPath(path) + " · turn " + fmt.Sprint(turn) + " · " + "run it with /recipe run " + name)
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
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
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

// finishRecipe handles recipeDoneMsg: it prints the run's report and, on
// a failed step, hands over to the model with agent.RecipeHandover — the
// same submit path a queued prompt or a goal round uses to start its next
// turn.
func (m *model) finishRecipe(msg recipeDoneMsg) tea.Cmd {
	m.running = false
	m.cancelling = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.setPhase("")
	if msg.report != "" {
		m.transcript.code(msg.report)
	}
	switch {
	case msg.err != nil:
		m.transcript.errorf("recipe %s: %v", msg.recipe.Name, msg.err)
		m.status = "error"
	case msg.failed != nil && msg.failed.Verify:
		m.note(fmt.Sprintf("recipe %s: steps all ran, but the project's checks failed; handing over to the model", msg.recipe.Name))
		m.term.turnEnded(false, m.status)
		m.refresh(false)
		return m.submit(agent.RecipeHandover(msg.recipe, *msg.failed))
	case msg.failed != nil:
		m.note(fmt.Sprintf("recipe %s stopped at step %d (%s); handing over to the model", msg.recipe.Name, msg.failed.Step, msg.failed.Tool))
		m.term.turnEnded(false, m.status)
		m.refresh(false)
		return m.submit(agent.RecipeHandover(msg.recipe, *msg.failed))
	default:
		m.status = "recipe " + msg.recipe.Name + " done"
	}
	m.term.turnEnded(msg.err == nil && msg.failed == nil, m.status)
	m.refresh(false)
	return nil
}
