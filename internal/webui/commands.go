package webui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
)

type cmdSpec struct {
	Name string `json:"name"`
	Args string `json:"args"`
	Help string `json:"help"`
	Idle bool   `json:"idle,omitempty"`
}

var commandSpecs = []cmdSpec{
	{Name: "/help", Args: "[topic]", Help: "keys and commands, or search the catalogue and the manual"},
	{Name: "/clear", Args: "", Help: "start a new session", Idle: true},
	{Name: "/compact", Args: "", Help: "summarise the conversation to free context", Idle: true},
	{Name: "/yolo", Args: "", Help: "toggle auto-approve for tools"},
	{Name: "/edits", Args: "", Help: "toggle auto-accept edits: file changes go through, commands still ask"},
	{Name: "/readonly", Args: "", Help: "toggle read-only mode (no edits or commands)"},
	{Name: "/plan", Args: "[request|approve|off]", Help: "plan mode: the model explores and writes a plan; approve executes it"},
	{Name: "/verbose", Args: "", Help: "toggle expanded tool output"},
	{Name: "/undo", Args: "[preview]", Help: "revert everything the last turn changed", Idle: true},
	{Name: "/redo", Args: "", Help: "reapply what the last /undo reverted", Idle: true},
	{Name: "/changes", Args: "[n]", Help: "this session's file changes"},
	{Name: "/diff", Args: "[path]", Help: "changes against HEAD, or one file's hunks"},
	{Name: "/open", Args: "<path[:line]>", Help: "show a file, centred on a line"},
	{Name: "/context", Args: "", Help: "what fills the context window"},
	{Name: "/stats", Args: "", Help: "session statistics: turns, tools, tokens, cost"},
	{Name: "/usage", Args: "", Help: "tokens and cost for this session"},
	{Name: "/budget", Args: "[usd|off]", Help: "stop once the session has spent this much", Idle: true},
	{Name: "/todos", Args: "", Help: "the plan the model is working from"},
	{Name: "/jobs", Args: "[kill n]", Help: "background commands"},
	{Name: "/memory", Args: "[query]", Help: "what janetcode remembers about this project"},
	{Name: "/remember", Args: "[-g] <fact>", Help: "add a fact to project (or global) memory"},
	{Name: "/forget", Args: "<n>", Help: "remove memory entry n"},
	{Name: "/skills", Args: "", Help: "skill files available to the model"},
	{Name: "/pin", Args: "[note or @file]", Help: "keep a note in the system prompt"},
	{Name: "/unpin", Args: "<n>", Help: "remove a pin"},
	{Name: "/title", Args: "<text>", Help: "name this session"},
	{Name: "/model", Args: "[name]", Help: "show the current model, or switch to another (any configured provider)"},
	{Name: "/fork", Args: "[title]", Help: "branch this conversation into a new session and continue there", Idle: true},
	{Name: "/settle", Args: "", Help: "mark this session done so it lists after active ones"},
	{Name: "/goal", Args: "[objective|pause|resume|done]", Help: "loop on an objective until it is complete or blocked"},
	{Name: "/why", Args: "[n]", Help: "explain the last thing the harness did on its own"},
	{Name: "/trajectory", Args: "[n|grep words]", Help: "the session's event ledger"},
	{Name: "/agents", Args: "[id]", Help: "subagents; /agents <id> shows that child's transcript"},
	{Name: "/reload", Args: "", Help: "rebuild the memory and skill prompt indexes"},
	{Name: "/inspect", Args: "[subject]", Help: "look at any part of the session"},
	{Name: "/permissions", Args: "", Help: "permission rules in effect"},
	{Name: "/export", Args: "[file.md]", Help: "write this session as a Markdown transcript"},
	{Name: "/feedback", Args: "up|down [note]", Help: "rate the last assistant message"},
}

func (s *server) commandsAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, commandSpecs)
}

func (s *server) runCommand(text string) error {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
	}
	name, arg := fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	ag := s.opts.Agent
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	for _, c := range commandSpecs {
		if c.Name == name && c.Idle && running {
			return fmt.Errorf("%s waits for the turn to finish (esc cancels it)", name)
		}
	}
	switch name {
	case "/help":
		s.help(arg)
	case "/clear", "/new":
		return s.startNewSession()
	case "/yolo":
		ag.AutoApprove = !ag.AutoApprove
		s.noteOnOff("auto-approve", ag.AutoApprove, "every tool runs without asking")
	case "/edits":
		ag.AutoEdits = !ag.AutoEdits
		s.noteOnOff("auto-accept edits", ag.AutoEdits, "file changes go through, commands still ask")
	case "/readonly":
		ag.ReadOnly = !ag.ReadOnly
		s.noteOnOff("read-only", ag.ReadOnly, "the model can inspect but change nothing")
	case "/plan":
		s.plan(arg)
	case "/verbose":
		s.mu.Lock()
		s.verbose = !s.verbose
		v := s.verbose
		s.mu.Unlock()
		s.noteOnOff("verbose tool output", v, "every tool result is shown whole")
	case "/compact":
		go func() { _ = ag.Compact(context.Background(), s.apply) }()
	case "/undo":
		if arg == "preview" {
			list, err := ag.UndoPreview()
			if err != nil {
				s.errf("undo: %v", err)
				return nil
			}
			s.sheet("undo preview", strings.Join(list, "\n"))
			return nil
		}
		done, err := ag.Undo()
		if err != nil {
			s.errf("undo: %v", err)
			return nil
		}
		s.notice("↶ " + strings.Join(done, ", "))
	case "/redo":
		done, err := ag.Redo()
		if err != nil {
			s.errf("redo: %v", err)
			return nil
		}
		s.notice("↷ " + strings.Join(done, ", "))
	case "/changes":
		s.sheet("changes", s.changesText(arg))
	case "/diff":
		s.sheet("diff", s.diffText(arg))
	case "/open":
		s.sheet("open", s.openText(arg))
	case "/context":
		s.sheet("context", s.contextText())
	case "/stats":
		s.sheet("stats", s.statsText())
	case "/usage":
		s.sheet("usage", s.usageText())
	case "/budget":
		s.budget(arg)
	case "/todos":
		s.sheet("todos", s.todosText())
	case "/jobs":
		s.jobs(arg)
	case "/memory":
		s.memory("/memory", arg)
	case "/remember":
		s.memory("/remember", arg)
	case "/forget":
		s.memory("/forget", arg)
	case "/skills":
		s.sheet("skills", s.skillsText())
	case "/pin":
		s.pin(arg)
	case "/unpin":
		n, err := strconv.Atoi(arg)
		if err != nil || !ag.Unpin(n) {
			s.errf("no pin %s; /pin lists them", arg)
			return nil
		}
		s.notice("unpinned")
	case "/title":
		if arg == "" || s.opts.Session == nil {
			s.errf("usage: /title <text>")
			return nil
		}
		if err := s.opts.Session.SetTitle(arg); err != nil {
			s.errf("title: %v", err)
			return nil
		}
		s.notice("titled: " + arg)
		s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	case "/inspect":
		s.inspect(arg)
	case "/model":
		s.model(arg)
	case "/fork":
		if err := s.fork(arg); err != nil {
			s.errf("fork: %v", err)
			return nil
		}
	case "/settle":
		if s.opts.Session == nil {
			s.errf("no session to settle")
			return nil
		}
		settled := !s.opts.Session.Meta.Settled
		if err := s.opts.Session.SetSettled(settled); err != nil {
			s.errf("settle: %v", err)
			return nil
		}
		if settled {
			s.notice("session settled: it lists after the active ones in the sidebar until something happens in it")
		} else {
			s.notice("session active again")
		}
	case "/goal":
		s.goal(arg)
	case "/why":
		n := 1
		if arg != "" {
			v, err := strconv.Atoi(arg)
			if err != nil || v < 1 {
				s.errf("usage: /why [n] · n counts back from the most recent")
				return nil
			}
			n = v
		}
		if s.opts.Session == nil {
			s.errf("no session")
			return nil
		}
		s.sheet("why", agent.WhyLast(s.opts.Session.Events(), n))
	case "/trajectory":
		s.trajectory(arg)
	case "/agents":
		s.agents(arg)
	case "/reload":
		ag.RefreshExtra()
		s.notice("memory and skill indexes will be rebuilt for the next call (one cache miss); they also refresh at compaction")
	case "/permissions":
		s.sheet("permissions", s.permissionsText())
	case "/export":
		s.export(arg)
	case "/feedback":
		s.notice("recorded " + arg + " (not sent to the model)")
	default:
		s.errf("unknown command %s (try /help)", name)
	}
	s.emitStatus()
	return nil
}

func (s *server) model(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		s.notice("model: " + ag.Model + " (" + s.opts.Provider + ") · /model <name> switches, across providers when the name routes elsewhere")
		return
	}
	cfg := s.opts.Config
	if cfg != nil && s.opts.Models != nil {
		// The name may belong to a provider other than the one in use;
		// follow it rather than sending it to the wrong endpoint.
		if name, ok := cfg.RouteFor(arg); ok && name != cfg.Provider {
			prov, _, err := s.opts.Models(arg)
			if err != nil {
				s.errf("%v", err)
				return
			}
			ag.Provider = prov
			cfg.Provider = name
			s.opts.Provider = name
			s.notice("switched to provider " + name + " for " + arg)
		}
	}
	ag.Model = arg
	if cfg != nil {
		ag.Price = cfg.PriceFor(arg)
		ag.ContextWindow = cfg.MetaFor(arg).ContextWindow
		cfg.Model = arg
		if s.opts.ConfigPath != "" {
			if err := config.Update(s.opts.ConfigPath, cfg.Provider, cfg.Active(), arg); err != nil {
				s.errf("saving config: %v", err)
			}
		}
	}
	s.notice("model set to " + arg + " on " + s.opts.Provider)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
}

// fork branches the conversation: the fork continues here, the original
// stays untouched on disk.
func (s *server) fork(title string) error {
	if s.opts.Session == nil {
		return fmt.Errorf("no session to fork")
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		return fmt.Errorf("wait for the current turn to finish before forking")
	}
	ns, err := session.Fork(filepath.Dir(s.opts.Session.Path()), s.opts.Session, title)
	if err != nil {
		return err
	}
	old := s.opts.Session.Label()
	s.opts.Session = ns
	ag := s.opts.Agent
	ag.SetLog(ns)
	ag.UndoPath = ns.UndoPath()
	ag.SaveUndo()
	s.notice(fmt.Sprintf("forked %s → %s · this session continues from the fork, the original is unchanged", old, ns.Meta.ID))
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	return nil
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
		var sb strings.Builder
		for _, g := range gs {
			fmt.Fprintf(&sb, "%s  %-8s  %d/%d  %s\n", g.ID, g.Phase, g.Rounds, g.MaxRounds, g.Objective)
			if g.Reason != "" {
				fmt.Fprintf(&sb, "    %s\n", g.Reason)
			}
		}
		s.sheet("goals", strings.TrimRight(sb.String(), "\n"))
	case "pause", "resume", "done":
		g := agent.ActiveGoal(ev)
		if arg == "resume" {
			g = agent.LastGoal(ev, agent.GoalPaused)
		}
		if g == nil {
			g = agent.LastGoal(ev, "")
		}
		if g == nil {
			s.errf("no goal")
			return
		}
		phase := agent.GoalPaused
		if arg == "resume" {
			phase = agent.GoalActive
		}
		if arg == "done" {
			phase = agent.GoalComplete
		}
		ag.SetGoalPhase(g.ID, phase, "")
		s.notice("goal " + g.ID + " " + string(phase))
		if phase == agent.GoalActive {
			s.goalRound()
		}
	default:
		id := ag.CreateGoal(arg, 0)
		s.notice("goal " + id + " active: " + arg)
		if s.opts.Session.Meta.Title == "" {
			_ = s.opts.Session.SetTitle(truncate(arg, 60))
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

func (s *server) trajectory(arg string) {
	if s.opts.Session == nil {
		s.errf("no session")
		return
	}
	ev := s.opts.Session.Events()
	if strings.HasPrefix(arg, "grep ") {
		q := strings.TrimSpace(strings.TrimPrefix(arg, "grep "))
		hits := session.QueryEvents(ev, session.QueryFilter{Text: q})
		if len(hits) == 0 {
			s.notice("no events matching " + q)
			return
		}
		var sb strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&sb, "#%d %s %s %s\n  %s\n", h.Event.Seq, h.Kind, h.Event.Type, h.Event.Name, h.Snippet)
		}
		s.sheet("trajectory · "+q, strings.TrimRight(sb.String(), "\n"))
		return
	}
	if n, err := strconv.Atoi(arg); err == nil && arg != "" {
		for _, e := range ev {
			if e.Seq != n {
				continue
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "seq %d  %s  turn %d step %d  %s\n", e.Seq, e.Type, e.Turn, e.Step, session.Classify(ev, e))
			if e.DurationMs > 0 {
				fmt.Fprintf(&sb, "duration %s\n", shortDuration(time.Duration(e.DurationMs)*time.Millisecond))
			}
			if e.Usage != nil {
				fmt.Fprintf(&sb, "usage in=%d cache=%d out=%d\n", e.Usage.InputTokens, e.Usage.CacheReadTokens, e.Usage.OutputTokens)
			}
			body := e.Output
			if body == "" && e.Message != nil {
				body = e.Message.Text()
			}
			sb.WriteString(truncate(body, 4000))
			s.sheet(fmt.Sprintf("event %d", n), sb.String())
			return
		}
		s.errf("no event seq %d", n)
		return
	}
	if len(ev) == 0 {
		s.notice("no trajectory events yet: no turn has run")
		return
	}
	hidden := session.Shadowed(ev)
	var sb strings.Builder
	fmt.Fprintf(&sb, "seq  turn/step  type                 name        dur    surface\n")
	for _, e := range ev {
		fmt.Fprintf(&sb, "%-4d %d/%-6d %-20s %-11s %7s %s\n", e.Seq, e.Turn, e.Step, e.Type, truncate(e.Name, 11),
			shortDuration(time.Duration(e.DurationMs)*time.Millisecond), session.ClassifyWith(hidden, e))
	}
	s.sheet("trajectory", strings.TrimRight(sb.String(), "\n"))
}

func (s *server) agents(id string) {
	task := s.taskTool()
	if task == nil {
		s.notice("no task tool")
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
		s.notice("no subagents yet; task continue=<id> reuses one")
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
func (s *server) help(topic string) {
	if topic != "" && s.opts.Features != nil {
		s.sheet("help · "+topic, feature.Answer(s.opts.Features, topic, ""))
		return
	}
	var sb strings.Builder
	sb.WriteString("Write a request and press Send. Shift+Enter starts a new line.\n")
	sb.WriteString("When a tool wants to change something, use the buttons (or Y / N).\n")
	sb.WriteString("Ctrl+K searches every command. A few you may want:\n\n")
	for _, c := range commandSpecs {
		fmt.Fprintf(&sb, "%-14s %-18s %s\n", c.Name, c.Args, c.Help)
	}
	s.sheet("help", sb.String())
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
		s.startTurn("Execute the plan you wrote, step by step, verifying each step.")
	default:
		ag.SetPlanMode(true)
		s.startTurn("Write a plan for: " + arg)
	}
}

func (s *server) budget(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		if ag.MaxCostUSD <= 0 {
			s.notice("no spend ceiling · /budget 1.50 sets one")
			return
		}
		u := ag.UsageSnapshot()
		spent, _ := u.Cost(ag.Price)
		s.notice(fmt.Sprintf("budget %s · spent %s · %s left", llm.FormatUSD(ag.MaxCostUSD), llm.FormatUSD(spent), llm.FormatUSD(ag.MaxCostUSD-spent)))
		return
	}
	if arg == "off" {
		ag.MaxCostUSD = 0
		s.notice("budget off")
		return
	}
	v, err := strconv.ParseFloat(arg, 64)
	if err != nil || v <= 0 {
		s.errf("usage: /budget <usd|off>")
		return
	}
	ag.MaxCostUSD = v
	s.notice("budget " + llm.FormatUSD(v))
}

func (s *server) jobs(arg string) {
	if s.opts.Jobs == nil {
		s.notice("no background jobs")
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 2 && fields[0] == "kill" {
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			s.errf("usage: /jobs kill <n>")
			return
		}
		if err := s.opts.Jobs.Kill(n); err != nil {
			s.errf("%v", err)
			return
		}
		s.notice(fmt.Sprintf("killed job %d", n))
		return
	}
	s.sheet("jobs", s.jobsText())
}

func (s *server) memory(cmd, arg string) {
	store := s.opts.Memory
	if store == nil {
		s.errf("memory is not available in this session")
		return
	}
	switch cmd {
	case "/remember":
		scope := "project"
		for _, flag := range []string{"-g ", "--global "} {
			if strings.HasPrefix(arg, flag) {
				scope, arg = "global", strings.TrimSpace(strings.TrimPrefix(arg, flag))
			}
		}
		if arg == "" {
			s.errf("usage: /remember [-g] <what to keep>")
			return
		}
		e, status, err := store.Put(memory.Entry{Body: arg, Scope: scope, Source: "user"})
		if err != nil {
			s.errf("remember: %v", err)
			return
		}
		switch status {
		case memory.Unchanged:
			s.notice("already remembered as " + e.Name)
		case memory.Updated:
			s.notice("memory updated: " + e.Name)
		default:
			s.notice("remembered: " + e.Name)
		}
	case "/forget":
		gone, err := store.Remove(arg)
		if err != nil {
			s.errf("forget: %v", err)
			return
		}
		s.notice("forgot " + gone.Name)
	default:
		s.sheet("memory", s.memoryText(arg))
	}
}

func (s *server) pin(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		if len(ag.Pins) == 0 {
			s.notice("no pins: /pin <note> or /pin @path")
			return
		}
		var sb strings.Builder
		for i, p := range ag.Pins {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, trunc(strings.ReplaceAll(p, "\n", " "), 120))
		}
		s.sheet("pins", sb.String())
		return
	}
	if strings.HasPrefix(arg, "@") && !strings.ContainsAny(arg, " \t") {
		rel := strings.TrimPrefix(arg, "@")
		data, err := os.ReadFile(filepath.Join(s.opts.Cwd, rel))
		if err != nil {
			s.errf("pin: %v", err)
			return
		}
		body := string(data)
		if len(body) > 16*1024 {
			body = body[:16*1024] + "\n… (truncated)"
		}
		arg = "File " + rel + " as of pinning:\n" + body
	}
	ag.Pin(arg)
	s.notice(fmt.Sprintf("pinned (%d)", len(ag.Pins)))
}

func (s *server) inspect(arg string) {
	if s.opts.Inspect == nil {
		s.errf("nothing is registered for inspection")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := s.opts.Inspect.Report(ctx, arg)
	if err != nil {
		s.errf("%v", err)
		return
	}
	title := "inspect"
	if arg != "" {
		title += " · " + arg
	}
	s.sheet(title, out)
}

func (s *server) export(path string) {
	if path == "" && s.opts.Session != nil {
		path = "janetcode-" + s.opts.Session.Meta.ID + ".md"
	}
	if path == "" {
		s.errf("usage: /export [file.md]")
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.opts.Cwd, path)
	}
	var sb strings.Builder
	for _, e := range s.snapshotEntries() {
		switch e.Kind {
		case "user":
			fmt.Fprintf(&sb, "## user\n\n%s\n\n", e.Text)
		case "assistant":
			fmt.Fprintf(&sb, "## assistant\n\n%s\n\n", e.Text)
		case "tool":
			fmt.Fprintf(&sb, "### %s %s\n\n```\n%s\n```\n\n", e.Name, e.Summary, e.Output)
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		s.errf("export: %v", err)
		return
	}
	s.notice("wrote " + path)
}

func (s *server) snapshotEntries() []*entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*entry(nil), s.entries...)
}

func (s *server) sheet(title, body string) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		body = "(" + title + " is empty)"
	}
	s.upsert(&entry{Kind: "sheet", Name: title, Text: body})
}

func (s *server) notice(text string) {
	s.emit(envelope{Type: "notice", Notice: text})
	s.upsert(&entry{Kind: "notice", Text: text})
}

func (s *server) errf(f string, a ...any) {
	s.upsert(&entry{Kind: "error", Text: fmt.Sprintf(f, a...), Error: true})
}

func (s *server) noteOnOff(name string, on bool, whenOn string) {
	if on {
		s.notice(name + " on · " + whenOn)
		return
	}
	s.notice(name + " off")
}

func (s *server) contextText() string {
	r := s.opts.Agent.Context()
	var sb strings.Builder
	pct := 0
	if r.CompactAt > 0 {
		pct = r.Total * 100 / r.CompactAt
	}
	fmt.Fprintf(&sb, "context ~%s tokens · compaction at %s (%d%%) · %d messages, %d turns\n", shortTokens(r.Total), shortTokens(r.CompactAt), pct, r.Messages, r.Turns)
	fmt.Fprintf(&sb, "  system %s   extra %s   pins %s (%d)\n", shortTokens(r.System), shortTokens(r.Extra), shortTokens(r.Pins), len(s.opts.Agent.Pins))
	fmt.Fprintf(&sb, "  tools  %s (%d)   user %s   assistant %s\n", shortTokens(r.Tools), r.ToolCount, shortTokens(r.UserText), shortTokens(r.AssistantText))
	fmt.Fprintf(&sb, "  tool calls %s   results %s   thinking %s\n", shortTokens(r.ToolCalls), shortTokens(r.ToolResults), shortTokens(r.Thinking))
	return sb.String()
}

func (s *server) statsText() string {
	ag := s.opts.Agent
	st := ag.Stats()
	u := ag.UsageSnapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s · %s\n", plural(st.Turns, "turn"), plural(st.ModelCalls, "model call"))
	if st.ModelTime > 0 && st.ModelCalls > 0 {
		fmt.Fprintf(&sb, "waiting on the model %s (avg %s)\n", shortDuration(st.ModelTime), shortDuration(st.ModelTime/time.Duration(st.ModelCalls)))
	}
	fmt.Fprintf(&sb, "tokens in %s · out %s · cache read %s · cache write %s\n",
		shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens))
	if usd, ok := u.Cost(ag.Price); ok {
		sb.WriteString("cost " + llm.FormatUSD(usd) + "\n")
	}
	for _, t := range st.Tools {
		fmt.Fprintf(&sb, "  %-16s %d calls", t.Name, t.Calls)
		if t.Errors > 0 {
			fmt.Fprintf(&sb, " (%d failed)", t.Errors)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (s *server) usageText() string {
	ag := s.opts.Agent
	u := ag.UsageSnapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "in %s · out %s · cache read %s · cache write %s",
		shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens))
	if usd, ok := u.Cost(ag.Price); ok {
		sb.WriteString(" · " + llm.FormatUSD(usd))
	}
	if ag.MaxCostUSD > 0 {
		sb.WriteString(" · ceiling " + llm.FormatUSD(ag.MaxCostUSD))
	}
	return sb.String()
}

func (s *server) todosText() string {
	todos := s.opts.Agent.Todos()
	if len(todos) == 0 {
		return "no plan yet · the model writes one with the todo tool"
	}
	var sb strings.Builder
	for _, t := range todos {
		mark := "·"
		switch t.Status {
		case agent.TodoDoing:
			mark = "▶"
		case agent.TodoDone:
			mark = "✓"
		}
		fmt.Fprintf(&sb, "%s %s\n", mark, t.Text)
	}
	return sb.String()
}

func (s *server) jobsText() string {
	if s.opts.Jobs == nil {
		return "background jobs are not available"
	}
	jobs := s.opts.Jobs.List()
	if len(jobs) == 0 {
		return "no background jobs · the model starts one with bash background:true"
	}
	var sb strings.Builder
	for _, j := range jobs {
		sb.WriteString(j.Summary() + "\n")
	}
	return sb.String()
}

func (s *server) memoryText(query string) string {
	if s.opts.Memory == nil {
		return "memory is not available"
	}
	entries := s.opts.Memory.Entries()
	if query != "" {
		hits := s.opts.Memory.Search(query, 8)
		if len(hits) == 0 {
			return "nothing remembered matches " + strconv.Quote(query)
		}
		entries = hits
	}
	if len(entries) == 0 {
		return "nothing remembered yet · /remember <fact> adds one"
	}
	var sb strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&sb, "%3d. %s (%s): %s\n", i+1, e.Name, e.Type, e.Description)
	}
	return sb.String()
}

func (s *server) skillsText() string {
	skills := skill.Load(s.opts.Cwd, s.opts.ConfigDir)
	if len(skills) == 0 {
		return "no skills found · add .janetcode/skills/<name>/SKILL.md"
	}
	var sb strings.Builder
	for _, sk := range skills {
		fmt.Fprintf(&sb, "%-20s %s  (%s)\n", sk.Name, sk.Description, sk.Source)
	}
	return sb.String()
}

func (s *server) changesText(arg string) string {
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		c, err := s.opts.Agent.Change(n)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("#%d %s %s\n%s", c.Seq, c.Tool, c.Path, c.After)
	}
	changes := s.opts.Agent.Changes()
	if len(changes) == 0 {
		return "no file changes in this session yet"
	}
	var sb strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&sb, "  #%-3d %-10s %s\n", c.Seq, c.Tool, c.Path)
	}
	return sb.String()
}

func (s *server) diffText(path string) string {
	args := []string{"-C", s.opts.Cwd, "diff", "--no-color"}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "git diff: " + err.Error()
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return "no changes against HEAD"
	}
	return string(out)
}

func (s *server) openText(arg string) string {
	if arg == "" {
		return "usage: /open path[:line]"
	}
	path, line := arg, 0
	if i := strings.LastIndexByte(arg, ':'); i > 0 {
		if n, err := strconv.Atoi(arg[i+1:]); err == nil {
			path, line = arg[:i], n
		}
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.opts.Cwd, path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	from, to := 1, len(lines)
	if line > 0 {
		from, to = line-30, line+30
		if from < 1 {
			from = 1
		}
		if to > len(lines) {
			to = len(lines)
		}
	} else if len(lines) > 120 {
		to = 120
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s  lines %d-%d of %d\n", path, from, to, len(lines))
	w := len(strconv.Itoa(to))
	for n := from; n <= to; n++ {
		mark := " "
		if n == line {
			mark = "▶"
		}
		fmt.Fprintf(&sb, "%s%*d │ %s\n", mark, w, n, lines[n-1])
	}
	return sb.String()
}

func (s *server) permissionsText() string {
	ag := s.opts.Agent
	var sb strings.Builder
	if ag.AutoApprove {
		sb.WriteString("YOLO · every tool runs without asking\n")
	}
	if ag.AutoEdits {
		sb.WriteString("auto-edits · file changes go through, commands still ask\n")
	}
	if ag.ReadOnly {
		sb.WriteString("read-only · mutating tools are hidden\n")
	}
	if ag.PlanMode {
		sb.WriteString("plan mode\n")
	}
	rs := ag.Rules
	if len(rs.Allow) == 0 && len(rs.Deny) == 0 {
		if sb.Len() == 0 {
			return "no saved rules · y/a/r at a prompt"
		}
		return sb.String()
	}
	for _, r := range rs.Deny {
		fmt.Fprintf(&sb, "deny  %s\n", r.String())
	}
	for _, r := range rs.Allow {
		fmt.Fprintf(&sb, "allow %s\n", r.String())
	}
	return sb.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func shortTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n >= 1000:
		return fmt.Sprintf("%.2fk", float64(n)/1000)
	default:
		return strconv.Itoa(n)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
