package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
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
		id := ag.CreateGoal(arg, 0)
		m.note("goal " + id + " active: " + arg)
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
	cont := ag.GoalContinuation()
	if cont == "" {
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
		m.ov.child = arg
		for i, k := range kids {
			if k.ID == arg {
				m.ov.idx = i
				break
			}
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
	diff, err := agent.ReviewDiff(context.Background(), m.opts.Cwd)
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
	send := m.send
	go func() {
		_, runErr := task.Start(context.Background(), in)
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
	cron := ag.Cron
	switch {
	case arg == "" || arg == "list":
		jobs := cron.List()
		if len(jobs) == 0 {
			m.note("no loops · /loop 5m <prompt> arms one")
			m.refresh(false)
			return
		}
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&b, "%s every %s next %s · %s\n", j.ID, j.Every, j.Next.Format("15:04:05"), j.Prompt)
		}
		m.transcript.info(strings.TrimRight(b.String(), "\n"))
	case arg == "off":
		n := cron.CancelAll()
		m.note(fmt.Sprintf("cancelled %s", textfmt.Plural(n, "loop")))
	case strings.HasPrefix(arg, "cancel "):
		id := strings.TrimSpace(strings.TrimPrefix(arg, "cancel "))
		if !cron.Cancel(id) {
			m.transcript.errorf("no loop %s", id)
		} else {
			m.note("cancelled " + id)
		}
	default:
		every := 5 * time.Minute
		prompt := arg
		if f, rest, ok := strings.Cut(arg, " "); ok {
			if d, err := agent.ParseEvery(f); err == nil {
				every, prompt = d, strings.TrimSpace(rest)
			}
		}
		id, err := cron.Add(every, prompt)
		if err != nil {
			m.transcript.errorf("loop: %v", err)
		} else {
			m.note(fmt.Sprintf("loop %s every %s · restored on resume of this session", id, every))
		}
	}
	m.refresh(false)
}
