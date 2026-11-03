package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/cron"
	"github.com/xmonader/janetcode/internal/textfmt"
)

func (m *model) goalCommand(arg string) tea.Cmd {
	ag := m.opts.Agent
	switch {
	case arg == "" || arg == "list":
		if ag.Log == nil {
			m.transcript.info("no session log")
			return nil
		}
		gs := agent.FoldGoals(ag.Log.Events())
		if len(gs) == 0 {
			m.transcript.info("no goals: /goal <objective> starts one")
			return nil
		}
		m.transcript.info(agent.FormatGoals(gs))
		return nil
	case arg == "pause" || arg == "resume" || arg == "done":
		if ag.Log == nil {
			m.transcript.errorf("no session log")
			return nil
		}
		g, phase, err := agent.TargetGoal(ag.Log.Events(), arg)
		if err != nil {
			m.transcript.errorf("%v", err)
			return nil
		}
		ag.SetGoalPhase(g.ID, phase, "")
		m.note("goal " + g.ID + " " + phase)
		m.refresh(false)
		if phase == agent.GoalActive {
			return m.startGoalRound()
		}
		return nil
	default:
		// "rounds=20 <objective>" sets the budget for a new goal, and
		// "rounds 20" on its own re-budgets the one already running —
		// which is the only way to carry on a goal whose rounds are
		// spent.
		rounds, rest, budgeted := agent.ParseGoalRounds(arg)
		if budgeted && rest == "" {
			g, _, err := agent.TargetGoal(ag.Log.Events(), "resume")
			if err != nil {
				m.transcript.errorf("%v", err)
				return nil
			}
			ag.SetGoalRounds(g.ID, rounds)
			m.note(fmt.Sprintf("goal %s: %d rounds (%d used)", g.ID, rounds, g.Rounds))
			m.refresh(false)
			return m.startGoalRound()
		}
		if budgeted {
			arg = rest
		}
		id, paused := ag.CreateGoal(arg, rounds)
		m.note("goal " + id + " active: " + arg)
		if len(paused) > 0 {
			m.note(agent.PausedNote(paused))
		}
		// The objective is what this session is about. Without this the
		// title was generated from the first message the model saw, which
		// for a goal is the continuation prompt: sessions came back from
		// /resume called "Work on this goal until it is complete or you
		// are blocked".
		if m.opts.Session != nil && m.opts.Session.Meta.Title == "" {
			m.titleGen++
			if err := m.opts.Session.SetTitle(cutWords(arg, 60)); err != nil {
				m.transcript.errorf("save title: %v", err)
			}
		}
		return m.startGoalRound()
	}
}

// startGoalRound begins the active goal's next round now when the model
// is idle. Setting a goal and seeing nothing happen was the alternative:
// the loop only advanced when a turn ended, and there was no turn.
func (m *model) startGoalRound() tea.Cmd {
	if m.turn.running {
		m.note("the goal continues when this turn ends")
		return nil
	}
	return m.goalRound()
}

// goalRound starts the active goal's next round, announcing it as one
// line naming the goal, the round and the objective.
func (m *model) goalRound() tea.Cmd {
	ag := m.opts.Agent
	var note string
	if ag.Log != nil {
		if g := agent.ActiveGoal(ag.Log.Events()); g != nil {
			note = fmt.Sprintf("%sgoal %s round %d/%d \u00b7 %s", noteGoal, g.ID, g.Rounds+1, g.MaxRounds, g.Objective)
		}
	}
	cont := ag.GoalContinuation(m.life)
	if cont == "" {
		// No round to send: either there is no active goal, or the one
		// there is has spent its budget and has to say so.
		if g, spent := ag.SpentGoal(); spent {
			m.note(agent.SpentNote(g))
			m.refresh(false)
		}
		return nil
	}
	if note == "" {
		note = noteGoal + "goal continuing"
	}
	return m.submitAs(cont, note)
}

// agentsCommand opens the subagent overlay, on one child when arg names
// it, or starts a new subagent for /agents new|fork <brief>.
func (m *model) agentsCommand(arg string) {
	in, start, err := agent.AgentsArg(arg)
	if err != nil {
		m.transcript.info(err.Error())
		return
	}
	if in.Cancel != "" {
		task := m.taskTool()
		if task == nil {
			m.transcript.errorf("no task tool in this session")
			return
		}
		if err := task.Cancel(in.Cancel); err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		m.note("cancelled " + in.Cancel)
		return
	}
	if in.Steer != "" {
		task := m.taskTool()
		if task == nil {
			m.transcript.errorf("no task tool in this session")
			return
		}
		if err := task.SteerChild(in.Steer, in.Prompt); err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		m.note("steering " + in.Steer)
		return
	}
	if start {
		task := m.taskTool()
		if task == nil {
			m.transcript.errorf("no task tool in this session")
			return
		}
		m.openOverlay(overlayAgents)
		m.startTask(task, in)
		m.note("started " + in.Kind() + " · " + in.Description + " · esc closes, it keeps running")
		return
	}
	m.openOverlay(overlayAgents)
	kids := m.overlayKids()
	switch {
	case arg != "":
		for i, k := range kids {
			if k.ID == arg {
				m.ov.idx, m.ov.child = i, arg
				break
			}
		}
		if m.ov.child == "" {
			// The list, saying so: it used to open as though the
			// subagent had been found.
			m.ov.note = "no subagent " + arg
		}
	case len(kids) == 1:
		m.ov.child = kids[0].ID
	}
}

func (m *model) reviewCommand(arg string) tea.Cmd {
	task := m.taskTool()
	if task == nil {
		m.transcript.errorf("no task tool in this session")
		m.refresh(false)
		return nil
	}
	diff, err := agent.ReviewDiff(m.life, m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("review: %v", err)
		m.refresh(false)
		return nil
	}
	if strings.TrimSpace(diff) == "" && arg == "" {
		m.note("nothing to review (working tree clean)")
		m.refresh(false)
		return nil
	}
	m.openOverlay(overlayAgents)
	for _, in := range agent.ReviewTasks(arg, diff) {
		m.startTask(task, in)
	}
	m.note("started review · two subagents · /agents watches them")
	m.refresh(false)
	return nil
}

// startTask runs a subagent in the background and reports when it ends.
func (m *model) startTask(task *agent.Task, in agent.TaskInput) {
	send, life := m.send, m.life
	go func() {
		_, runErr := task.Start(life, in)
		send(subagentDoneMsg{desc: in.Description, err: runErr})
	}()
}

func (m *model) loopCommand(arg string) {
	ag := m.opts.Agent
	if ag == nil || ag.Cron == nil {
		m.transcript.errorf("no scheduler in this session")
		m.refresh(false)
		return
	}
	sched := ag.Cron
	switch {
	case arg == "" || arg == "list":
		jobs := sched.List()
		if len(jobs) == 0 {
			m.note("no loops · /loop 5m <prompt> arms one")
			m.refresh(false)
			return
		}
		var b strings.Builder
		for _, j := range jobs {
			b.WriteString(j.Line() + "\n")
		}
		m.transcript.info(strings.TrimRight(b.String(), "\n"))
	case arg == "off":
		n := sched.CancelAll()
		m.note(fmt.Sprintf("cancelled %s", textfmt.Plural(n, "loop")))
	case arg == "cancel":
		// Without an id this is not a prompt either: it armed a loop
		// that sent the word "cancel" every five minutes.
		m.transcript.errorf("usage: /loop cancel <id> (/loop lists them; /loop off cancels all)")
	case strings.HasPrefix(arg, "cancel "):
		id := strings.TrimSpace(strings.TrimPrefix(arg, "cancel "))
		if !sched.Cancel(id) {
			m.transcript.errorf("no loop %s", id)
		} else {
			m.note("cancelled " + id)
		}
	default:
		if msg, ok, err := sched.Control(arg); ok {
			if err != nil {
				m.transcript.errorf("loop: %v", err)
			} else {
				m.note(msg)
			}
			break
		}
		arg, observe := cron.CutObserve(arg)
		mode := cron.ModeRun
		if observe {
			mode = cron.ModeObserve
		}
		every, prompt, err := agent.LoopArg(arg)
		if err != nil {
			m.transcript.errorf("loop: %v", err)
			break
		}
		id, err := sched.AddMode(every, prompt, mode)
		if err != nil {
			m.transcript.errorf("loop: %v", err)
		} else {
			m.note(fmt.Sprintf("loop %s every %s · restored on resume of this session", id, every))
		}
	}
	m.refresh(false)
}
