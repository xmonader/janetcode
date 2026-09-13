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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	if s.opts.Session == nil {
		s.errf("no session")
		return
	}
	ev := s.opts.Session.Events()
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
		id := ag.CreateGoal(arg, 0)
		s.notice("goal " + id + " active: " + arg)
		if s.opts.Session.Meta.Title == "" {
			_ = s.opts.Session.SetTitle(textfmt.Clip(arg, 60))
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
	cont := s.opts.Agent.GoalContinuation()
	if cont == "" {
		return
	}
	s.startTurn(cont)
}

// startTask runs a subagent in the background and reports when it ends.
func (s *server) startTask(task *agent.Task, kind string, in agent.TaskInput) {
	go func() {
		_, runErr := task.Start(context.Background(), in)
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
	diff, err := agent.ReviewDiff(s.opts.Cwd)
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
	case strings.HasPrefix(arg, "cancel "):
		id := strings.TrimSpace(strings.TrimPrefix(arg, "cancel "))
		if !cron.Cancel(id) {
			s.errf("no loop %s", id)
			return
		}
		s.notice("cancelled " + id)
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
	sb.WriteString("\n/agents <id> shows that child's transcript · steer one by asking: \"tell task-2 to also check …\"")
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
					body = strings.Join(lines[:8], "\n") + fmt.Sprintf("\n… %d more lines\n", n-12) + strings.Join(lines[len(lines)-4:], "\n")
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

func (s *server) plan(arg string) {
	ag := s.opts.Agent
	switch arg {
	case "", "on":
		ag.SetPlanMode(true)
		s.notice("plan mode on · the model explores and writes a plan; /plan approve runs it")
	case "off":
		ag.SetPlanMode(false)
		s.notice("plan mode off")
	case "approve":
		ag.SetPlanMode(false)
		s.notice("plan approved · executing it")
		s.startTurn(agent.PlanApprovedPrompt)
	default:
		ag.SetPlanMode(true)
		s.startTurn("Write a plan for: " + arg)
	}
}
