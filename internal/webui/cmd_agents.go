package webui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
)

func (s *server) btw(q string) {
	q = strings.TrimSpace(q)
	if q == "" {
		s.errf("usage: /btw <question>")
		return
	}
	ag := s.opts.Agent
	go func() {
		ctx, cancel := context.WithTimeout(s.life, 2*time.Minute)
		defer cancel()
		a, err := ag.AskOnce(ctx, q)
		if err != nil {
			s.errf("btw: %v", err)
			return
		}
		s.sheet("btw "+q, a)
	}()
}

func (s *server) goal(arg string) {
	ag := s.opts.Agent
	if s.sess() == nil {
		s.errf("no session")
		return
	}
	ev := s.sess().Events()
	switch arg {
	case "", "list":
		gs := agent.FoldGoals(ev)
		if len(gs) == 0 {
			s.notice("no goals: /goal <objective> starts one")
			return
		}
		s.sheet("goals", agent.FormatGoals(gs))
	case "pause", "resume", "done":
		g, phase, err := agent.TargetGoal(ev, arg)
		if err != nil {
			s.errf("%v", err)
			return
		}
		ag.SetGoalPhase(g.ID, phase, "")
		s.notice("goal " + g.ID + " " + phase)
		if phase == agent.GoalActive {
			s.goalRound()
		}
	default:
		// "rounds=20 <objective>" budgets a new goal; "rounds 20" alone
		// re-budgets the running one, which is the only way to carry on
		// a goal whose rounds are spent.
		rounds, rest, budgeted := agent.ParseGoalRounds(arg)
		if budgeted && rest == "" {
			g, _, err := agent.TargetGoal(ev, "resume")
			if err != nil {
				s.errf("%v", err)
				return
			}
			ag.SetGoalRounds(g.ID, rounds)
			s.notice(fmt.Sprintf("goal %s: %d rounds (%d used)", g.ID, rounds, g.Rounds))
			s.goalRound()
			return
		}
		if budgeted {
			arg = rest
		}
		id, paused := ag.CreateGoal(arg, rounds)
		s.notice("goal " + id + " active: " + arg)
		if len(paused) > 0 {
			s.notice(agent.PausedNote(paused))
		}
		if s.sess().Title() == "" {
			_ = s.sess().SetTitle(textfmt.Clip(arg, 60))
		}
		s.goalRound()
	}
}

// goalRound starts the active goal's next round when idle.
func (s *server) goalRound() {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		s.notice("the goal continues when this turn ends")
		return
	}
	cont := s.opts.Agent.GoalContinuation(s.life)
	if cont == "" {
		if g, spent := s.opts.Agent.SpentGoal(); spent {
			s.notice(agent.SpentNote(g))
		}
		return
	}
	s.startHarnessTurn(cont)
}

// startTask runs a subagent in the background and reports when it ends.
func (s *server) startTask(task *agent.Task, kind string, in agent.TaskInput) {
	go func() {
		_, runErr := task.Start(s.life, in)
		if runErr != nil {
			s.errf("%s %s: %v", kind, in.Description, runErr)
			return
		}
		s.notice(kind + " finished · " + in.Description + " · /agents to read it")
	}()
}

func (s *server) review(arg string) {
	task := s.taskTool()
	if task == nil {
		s.notice("no task tool")
		return
	}
	diff, err := agent.ReviewDiff(s.life, s.cwd())
	if err != nil {
		s.errf("review: %v", err)
		return
	}
	if strings.TrimSpace(diff) == "" && arg == "" {
		s.notice("nothing to review (working tree clean)")
		return
	}
	for _, in := range agent.ReviewTasks(arg, diff) {
		s.startTask(task, "review", in)
	}
	s.notice("started review · two subagents · /agents watches them")
}

func (s *server) loop(arg string) {
	ag := s.opts.Agent
	if ag == nil || ag.Cron == nil {
		s.errf("no scheduler in this session")
		return
	}
	cron := ag.Cron
	switch {
	case arg == "" || arg == "list":
		jobs := cron.List()
		if len(jobs) == 0 {
			s.notice("no loops · /loop 5m <prompt> arms one for this session")
			return
		}
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&b, "%s every %s next %s · %s\n", j.ID, j.Every, j.Next.Format("15:04:05"), j.Prompt)
		}
		s.sheet("loop", strings.TrimRight(b.String(), "\n"))
	case arg == "off":
		n := cron.CancelAll()
		s.notice(fmt.Sprintf("cancelled %d loops", n))
	case arg == "cancel":
		s.errf("usage: /loop cancel <id> (/loop lists them; /loop off cancels all)")
	case strings.HasPrefix(arg, "cancel "):
		id := strings.TrimSpace(strings.TrimPrefix(arg, "cancel "))
		if !cron.Cancel(id) {
			s.errf("no loop %s", id)
			return
		}
		s.notice("cancelled " + id)
	default:
		every, prompt, err := agent.LoopArg(arg)
		if err != nil {
			s.errf("loop: %v", err)
			return
		}
		id, err := cron.Add(every, prompt)
		if err != nil {
			s.errf("loop: %v", err)
			return
		}
		s.notice(fmt.Sprintf("loop %s every %s · restored on resume of this session", id, every))
	}
}

// agents lists the subagents, shows one child's log when id names it, or
// starts a new subagent for /agents new|fork <brief>.
func (s *server) agents(id string) {
	task := s.taskTool()
	if task == nil {
		s.notice("no task tool")
		return
	}
	in, start, err := agent.AgentsArg(id)
	if err != nil {
		s.errf("%v", err)
		return
	}
	if in.Cancel != "" {
		if err := task.Cancel(in.Cancel); err != nil {
			s.errf("%v", err)
			return
		}
		s.notice("cancelled " + in.Cancel)
		return
	}
	if in.Steer != "" {
		if err := task.SteerChild(in.Steer, in.Prompt); err != nil {
			s.errf("%v", err)
			return
		}
		s.notice("steering " + in.Steer)
		return
	}
	if start {
		s.notice("started " + in.Kind() + " · " + in.Description)
		s.startTask(task, in.Kind(), in)
		return
	}
	if id != "" {
		log, ok := task.ChildLog(id)
		if !ok {
			s.notice("no subagent " + id)
			return
		}
		s.sheet(id, formatChildLog(log))
		return
	}
	kids := task.Children()
	if len(kids) == 0 {
		s.notice("no subagents yet · /agents new <brief> starts one")
		return
	}
	var sb strings.Builder
	for _, k := range kids {
		state := "done"
		if !k.Done {
			state = "running"
		}
		fmt.Fprintf(&sb, "%s  %-7s  %s\n", k.ID, state, k.Description)
		if !k.Done && k.Last != "" {
			fmt.Fprintf(&sb, "    now: %s\n", k.Last)
		}
		if k.Usage.InputTokens+k.Usage.OutputTokens > 0 {
			fmt.Fprintf(&sb, "    tokens in=%d out=%d\n", k.Usage.InputTokens, k.Usage.OutputTokens)
		}
	}
	sb.WriteString("\n/agents <id> shows that child's transcript · /agents cancel <id> stops a running one")
	s.sheet("subagents", strings.TrimRight(sb.String(), "\n"))
}

func formatChildLog(log agent.ChildLog) string {
	var sb strings.Builder
	state := "done"
	if !log.Done {
		state = "running"
	}
	fmt.Fprintf(&sb, "%s  %s  %s\n", log.ID, state, log.Description)
	if !log.Done && log.Last != "" {
		fmt.Fprintf(&sb, "now: %s\n", log.Last)
	}
	sb.WriteString("\n")
	for _, st := range log.Steps() {
		switch st.Kind {
		case "prompt":
			fmt.Fprintf(&sb, "❯ %s\n\n", st.Text)
		case "think":
			first, _, _ := strings.Cut(st.Text, "\n")
			fmt.Fprintf(&sb, "· %s\n", first)
		case "text":
			fmt.Fprintf(&sb, "%s\n\n", st.Text)
		case "tool":
			mark := "…"
			if !st.Pending && st.OK {
				mark = "✓"
			} else if !st.Pending {
				mark = "✗"
			}
			fmt.Fprintf(&sb, "%s %s %s\n", mark, st.Name, st.Text)
			if !st.Pending && st.Result != "" {
				body := st.Result
				if n := strings.Count(body, "\n"); n > 16 {
					lines := strings.Split(body, "\n")
					body = strings.Join(lines[:8], "\n") + fmt.Sprintf("\n… %d more lines\n", len(lines)-12) + strings.Join(lines[len(lines)-4:], "\n")
				}
				sb.WriteString(body)
				if !strings.HasSuffix(body, "\n") {
					sb.WriteByte('\n')
				}
				sb.WriteByte('\n')
			}
		case "now":
			fmt.Fprintf(&sb, "now: %s\n", st.Text)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// plan is the web UI's half of /plan: agent.PlanCommand decides
// everything, this renders the Result and starts the turn it hands over.
// The plan itself is the file .janetcode/plan.md, shown here as a sheet
// with line numbers, and a comment names a line because a browser has no
// cursor in it.
func (s *server) plan(arg string) {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	res, err := s.opts.Agent.PlanCommand(arg, running)
	s.showResult(res, err)
	if err == nil && res.Handover != "" {
		s.startTurn(res.Handover)
	}
}
