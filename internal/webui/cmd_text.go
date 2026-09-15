package webui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/textfmt"
)

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

func (s *server) doctor() {
	if s.opts.Doctor == nil {
		s.notice("run janetcode --doctor for the checkup")
		return
	}
	var buf strings.Builder
	err := s.opts.Doctor(&buf)
	report := strings.TrimRight(buf.String(), "\n")
	if report != "" {
		s.sheet("doctor", report)
	}
	if err != nil {
		s.errf("doctor: %v", err)
	}
}

func (s *server) inspect(arg string) {
	if s.opts.Inspect == nil {
		s.errf("nothing is registered for inspection")
		return
	}
	ctx, cancel := context.WithTimeout(s.life, 30*time.Second)
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

func (s *server) budget(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		ceiling := ag.MaxCost()
		if ceiling <= 0 {
			s.notice("no spend ceiling · /budget 1.50 sets one")
			return
		}
		u := ag.UsageSnapshot()
		spent, _ := u.Cost(ag.Settings().Price)
		s.notice(fmt.Sprintf("budget %s · spent %s · %s left", llm.FormatUSD(ceiling), llm.FormatUSD(spent), llm.FormatUSD(ceiling-spent)))
		return
	}
	v, err := agent.ParseBudget(arg)
	if err != nil {
		s.errf("%v", err)
		return
	}
	if v == 0 {
		ag.SetMaxCost(0)
		s.notice("budget off")
		return
	}
	ag.SetMaxCost(v)
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

func (s *server) copyLast() {
	ag := s.opts.Agent
	if ag == nil {
		s.errf("no assistant reply to copy")
		return
	}
	text := ag.LastAssistantText()
	if text == "" {
		s.errf("no assistant reply to copy")
		return
	}
	s.sheet("last reply", text)
}

func (s *server) contextText() string {
	r := s.opts.Agent.Context()
	var sb strings.Builder
	pct := 0
	if r.CompactAt > 0 {
		pct = r.Total * 100 / r.CompactAt
	}
	fmt.Fprintf(&sb, "context ~%s tokens · compaction at %s (%d%%) · %d messages, %d turns\n", textfmt.Tokens(r.Total), textfmt.Tokens(r.CompactAt), pct, r.Messages, r.Turns)
	fmt.Fprintf(&sb, "  system %s   extra %s   pins %s (%d)\n", textfmt.Tokens(r.System), textfmt.Tokens(r.Extra), textfmt.Tokens(r.Pins), len(s.opts.Agent.PinsSnapshot()))
	fmt.Fprintf(&sb, "  tools  %s (%d)   user %s   assistant %s\n", textfmt.Tokens(r.Tools), r.ToolCount, textfmt.Tokens(r.UserText), textfmt.Tokens(r.AssistantText))
	fmt.Fprintf(&sb, "  tool calls %s   results %s   thinking %s\n", textfmt.Tokens(r.ToolCalls), textfmt.Tokens(r.ToolResults), textfmt.Tokens(r.Thinking))
	return sb.String()
}

func (s *server) statsText() string {
	ag := s.opts.Agent
	st := ag.Stats()
	u := ag.UsageSnapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s · %s\n", textfmt.Plural(st.Turns, "turn"), textfmt.Plural(st.ModelCalls, "model call"))
	if st.ModelTime > 0 && st.ModelCalls > 0 {
		fmt.Fprintf(&sb, "waiting on the model %s (avg %s)\n", textfmt.Duration(st.ModelTime), textfmt.Duration(st.ModelTime/time.Duration(st.ModelCalls)))
	}
	sb.WriteString("tokens " + u.TokenLine() + "\n")
	if usd, ok := u.Cost(ag.Settings().Price); ok {
		sb.WriteString("cost " + llm.FormatUSD(usd) + "\n")
	}
	if line := st.PromptLine(); line != "" {
		sb.WriteString(line + "\n")
	}
	if line := st.RouteLine(ag.RouteCheapName()); line != "" {
		sb.WriteString(line + "\n")
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
	sb.WriteString(u.TokenLine() + "\n" + u.CostNote(ag.Settings().Price))
	if ceiling := ag.MaxCost(); ceiling > 0 {
		sb.WriteString(" · ceiling " + llm.FormatUSD(ceiling))
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
		return agent.FormatChange(c, s.opts.Cwd)
	}
	groups := s.opts.Agent.ChangeGroups()
	if len(groups) == 0 {
		return "no file changes in this session yet"
	}
	return agent.FormatChangeGroups(groups, s.opts.Cwd, s.opts.Agent.RestorableTurns())
}

func (s *server) diffText(path string) string {
	args := []string{"diff", "--no-color"}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := gitx.RunBytes(s.life, s.opts.Cwd, nil, args...)
	if err != nil {
		return err.Error()
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
	modes := ag.Settings()
	if modes.AutoApprove {
		sb.WriteString("YOLO · every tool runs without asking\n")
	}
	if modes.AutoMode {
		sb.WriteString("AUTO · in-workspace edits and known-safe reads run, other commands ask\n")
	}
	if modes.AutoEdits {
		sb.WriteString("auto-edits · file changes go through, commands still ask\n")
	}
	if modes.ReadOnly {
		sb.WriteString("read-only · mutating tools are hidden\n")
	}
	if modes.PlanMode {
		sb.WriteString("plan mode\n")
	}
	rs := ag.RulesSnapshot()
	switch {
	case len(rs.Allow) == 0 && len(rs.Deny) == 0 && sb.Len() == 0:
		sb.WriteString("no saved rules · y/a/r at a prompt\n")
	default:
		for _, r := range rs.Deny {
			fmt.Fprintf(&sb, "deny  %s\n", r.String())
		}
		for _, r := range rs.Allow {
			fmt.Fprintf(&sb, "allow %s\n", r.String())
		}
	}
	if lines := perm.CandidateLines(ag.Ledger, rs); len(lines) > 0 {
		sb.WriteString("approved often, not saved:\n")
		for _, l := range lines {
			fmt.Fprintf(&sb, "  %s\n", l)
		}
	}
	return sb.String()
}
