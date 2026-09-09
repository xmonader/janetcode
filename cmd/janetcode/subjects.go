package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// Every part of a running session registers what it can say about itself
// here, so "why did it do that" is answered by looking rather than by
// guessing. Subjects report the live state, not a copy of it.

// subjectDeps is what the subjects read.
type subjectDeps struct {
	agent   *agent.Agent
	session *session.Session
	cfg     *config.Config
	cfgPath string
	cwd     string
	version string
	archive *agent.Archive
	mem     *memory.Store
	repo    *repomap.Cache
	diag    *lsp.Manager
	mcp     *mcp.Manager
	jobs    *tool.Jobs
	task    *agent.Task
	skills  func() []skill.Skill
}

// registerSubjects wires every subsystem into the registry. A subsystem
// that is not configured still registers and says so, so the list of what
// can be inspected does not change from session to session.
func registerSubjects(r *inspect.Registry, d subjectDeps) {
	r.AddText("context", "what the next request will carry, by part", func() string { return contextReport(d.agent) })
	r.AddText("session", "the session file, its metadata and size", func() string { return sessionReport(d.session, d.agent) })
	r.Add(inspect.Subject{
		Name: "events", Summary: "the append-only log of the session", Usage: "[N|type|seq]",
		Report: func(_ context.Context, arg string) (string, error) { return eventsReport(d.session, arg), nil },
	})
	r.Add(inspect.Subject{
		Name: "archive", Summary: "the full tool outputs kept for retrieval", Usage: "[id]",
		Report: func(_ context.Context, arg string) (string, error) { return archiveReport(d.archive, arg), nil },
	})
	r.AddText("retrieval", "every archive lookup this session made, and whether it found anything", func() string {
		return retrievalReport(d.session)
	})
	r.AddText("compaction", "when the history was summarised and what it cost", func() string { return compactionReport(d.session) })
	r.AddText("verdicts", "what each turn actually demonstrated, against what was claimed", func() string {
		return verdictsReport(d.session)
	})
	r.AddText("todos", "the plan the model is working from", func() string { return todosReport(d.agent) })
	r.AddText("goals", "session goals and their rounds", func() string { return goalsReport(d.agent) })
	r.AddText("memory", "the working memory carried across compactions", func() string { return workingReport(d.agent) })
	r.AddText("tools", "the tools the model can call, with their sizes", func() string { return toolsReport(d.agent) })
	r.AddText("stats", "tool calls, model calls and timings so far", func() string { return statsReport(d.agent) })
	r.AddText("trajectory", "the features the critic scores this turn on", func() string { return trajectoryReport(d.agent) })
	r.AddText("config", "the resolved configuration and where it came from", func() string {
		return configReport(d.cfg, d.cfgPath, d.version, d.cwd)
	})
	r.AddText("permissions", "what may run without asking", func() string { return permissionsReport(d.agent, d.cfg) })
	r.AddText("lsp", "language servers, their state and known problems", func() string { return lspReport(d.diag) })
	r.AddText("mcp", "MCP servers and the tools they contributed", func() string { return mcpReport(d.mcp) })
	r.AddText("jobs", "background jobs, running and finished", func() string { return jobsReport(d.jobs) })
	r.AddText("subagents", "subagents that can be continued, and what they spent", func() string { return subagentsReport(d.task) })
	r.AddText("skills", "the skills discovered for this project", func() string { return skillsReport(d.skills) })
	r.AddText("notes", "the remembered notes available to the model", func() string { return notesReport(d.mem) })
	r.Add(inspect.Subject{
		Name: "repomap", Summary: "the repository map and how files rank", Usage: "[query]",
		Report: func(ctx context.Context, arg string) (string, error) { return repomapReport(ctx, d.repo, arg) },
	})
	r.AddText("undo", "file changes that can still be undone", func() string { return undoReport(d.agent) })
	r.Add(inspect.Subject{
		Name: "messages", Summary: "the conversation as the model will see it", Usage: "[N|#index]",
		Report: func(_ context.Context, arg string) (string, error) { return messagesReport(d.agent, arg), nil },
	})
	r.AddText("prompt", "the system prompt, verbatim", func() string { return promptReport(d.agent) })
	r.AddText("hooks", "the hooks that can interrupt a turn", func() string { return hooksReport(d.agent) })
	r.AddText("verify", "the checks a turn must pass before it is done", func() string { return verifyReport(d.agent) })
	r.AddText("providers", "the model endpoint in use and how requests are shaped", func() string {
		return providersReport(d.cfg, d.agent)
	})
	r.AddText("all", "every subject at once", func() string { return allReport(r) })
}

// messagesReport is the conversation as the model will receive it, which
// is not what the transcript shows: pruned results, injected notes and
// compaction summaries are all in here and none of them were typed.
func messagesReport(a *agent.Agent, arg string) string {
	if a == nil {
		return "no agent"
	}
	msgs := a.Messages
	if len(msgs) == 0 {
		return "the conversation is empty"
	}
	if strings.HasPrefix(arg, "#") {
		i, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if err != nil || i < 1 || i > len(msgs) {
			return fmt.Sprintf("no message #%s (there are %d)", strings.TrimPrefix(arg, "#"), len(msgs))
		}
		m := msgs[i-1]
		var b strings.Builder
		fmt.Fprintf(&b, "message %d of %d · %s\n\n", i, len(msgs), m.Role)
		for _, blk := range m.Blocks {
			switch blk.Type {
			case llm.BlockText, llm.BlockThinking:
				fmt.Fprintf(&b, "%s\n", trunc(blk.Text, 6000))
			case llm.BlockToolUse:
				fmt.Fprintf(&b, "[call %s %s] %s\n", blk.Name, blk.ID, trunc(string(blk.Input), 1000))
			case llm.BlockToolResult:
				tag := "result"
				if blk.IsError {
					tag = "error"
				}
				fmt.Fprintf(&b, "[%s %s]\n%s\n", tag, blk.ToolUseID, trunc(blk.Text, 4000))
			case llm.BlockImage:
				fmt.Fprintf(&b, "[image %s]\n", blk.MediaType)
			}
		}
		return b.String()
	}
	limit := 30
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		limit = n
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d messages (inspect messages #<n> for one in full)\n", len(msgs))
	from := 0
	if len(msgs) > limit {
		from = len(msgs) - limit
	}
	for i := from; i < len(msgs); i++ {
		m := msgs[i]
		kinds := map[string]int{}
		for _, blk := range m.Blocks {
			kinds[string(blk.Type)]++
		}
		var parts []string
		for _, k := range []string{"text", "thinking", "tool_use", "tool_result", "image"} {
			if kinds[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", kinds[k], k))
			}
		}
		fmt.Fprintf(&b, "  %-4d %-9s %-28s %s\n", i+1, m.Role, strings.Join(parts, ", "), trunc(oneLine(m.Text()), 60))
	}
	return b.String()
}

func promptReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	out := a.System
	if extra := a.SystemExtra; extra != nil {
		if e := strings.TrimSpace(extra()); e != "" {
			out += "\n\n" + e
		}
	}
	if len(a.Pins) > 0 {
		out += "\n\n# Pins\n"
		for _, p := range a.Pins {
			out += "- " + p + "\n"
		}
	}
	return out
}

func hooksReport(a *agent.Agent) string {
	if a == nil || a.Hooks == nil || len(a.Hooks.Config) == 0 {
		return "no hooks are configured"
	}
	events := make([]string, 0, len(a.Hooks.Config))
	for e := range a.Hooks.Config {
		events = append(events, e)
	}
	sort.Strings(events)
	var b strings.Builder
	fmt.Fprintf(&b, "hooks run in %s:\n", a.Hooks.Cwd)
	for _, e := range events {
		for _, h := range a.Hooks.Config[e] {
			fmt.Fprintf(&b, "  %-14s %s\n", e, trunc(h.Command, 80))
		}
	}
	return b.String()
}

func verifyReport(a *agent.Agent) string {
	if a == nil || a.Verify == nil || !a.Verify.Enabled() {
		return "no verification is configured for this project"
	}
	c := a.Verify.Config
	var b strings.Builder
	b.WriteString("a turn is not done until these pass:\n")
	for _, cmd := range c.Commands {
		fmt.Fprintf(&b, "  %s\n", cmd)
	}
	if c.Timeout > 0 {
		fmt.Fprintf(&b, "timeout %ds per command\n", c.Timeout)
	}
	if strings.TrimSpace(c.Mutate) != "" {
		fmt.Fprintf(&b, "\nmutation testing with: %s\n", c.Mutate)
		if c.MaxMutants > 0 {
			fmt.Fprintf(&b, "at most %d mutants per run\n", c.MaxMutants)
		}
	}
	return b.String()
}

func providersReport(cfg *config.Config, a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	var b strings.Builder
	name := "(none)"
	if a.Provider != nil {
		name = a.Provider.Name()
	}
	fmt.Fprintf(&b, "provider   %s\n", name)
	fmt.Fprintf(&b, "model      %s\n", a.Model)
	fmt.Fprintf(&b, "max tokens %d\n", a.MaxTokens)
	if cfg != nil {
		if m := cfg.MetaFor(a.Model); m.ContextWindow > 0 {
			fmt.Fprintf(&b, "window     %s\n", tokensOf(m.ContextWindow))
		}
		if p := cfg.PriceFor(a.Model); p != nil {
			fmt.Fprintf(&b, "price      $%.2f in / $%.2f out per million\n", p.Input, p.Output)
		}
	}
	u := a.UsageSnapshot()
	if pct, ok := a.CacheRate(); ok {
		fmt.Fprintf(&b, "cache      %d%% of input served from cache\n", pct)
	}
	if usd, ok := u.Cost(a.Price); ok {
		fmt.Fprintf(&b, "spent      %s\n", llm.FormatUSD(usd))
	}
	if cfg != nil {
		if routes := cfg.RoutableModels(); len(routes) > 0 {
			b.WriteString("\nmodels reachable by name (task can send a job to one):\n")
			for _, r := range routes {
				fmt.Fprintf(&b, "  %-24s %-18s %s\n", r.Model, r.Provider, r.Why)
			}
		}
		b.WriteString("\nany other name is routed by its family (glm-* to Zhipu, claude-* to Anthropic…)\n")
		b.WriteString("when that provider is configured, and otherwise to the active one.\n")
	}
	return b.String()
}

// allReport renders every subject, for a transcript of the whole state.
func allReport(r *inspect.Registry) string {
	var b strings.Builder
	for _, s := range r.Subjects() {
		if s.Name == "all" {
			continue
		}
		out, err := s.Report(context.Background(), "")
		if err != nil {
			out = "error: " + err.Error()
		}
		fmt.Fprintf(&b, "\n=== %s — %s ===\n%s\n", s.Name, s.Summary, strings.TrimRight(out, "\n"))
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func contextReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	c := a.Context()
	var b strings.Builder
	fmt.Fprintf(&b, "estimated %s of %s before compaction (%d%%)\n\n", tokensOf(c.Total), tokensOf(c.CompactAt), pct(c.Total, c.CompactAt))
	rows := []struct {
		name string
		n    int
	}{
		{"system prompt", c.System}, {"project context", c.Extra}, {"pins", c.Pins},
		{fmt.Sprintf("tool schemas (%d tools)", c.ToolCount), c.Tools}, {"deferred tools", c.Deferred},
		{"your messages", c.UserText}, {"assistant text", c.AssistantText}, {"reasoning", c.Thinking},
		{"tool calls", c.ToolCalls}, {"tool results", c.ToolResults}, {"images", c.Images},
	}
	for _, row := range rows {
		if row.n > 0 {
			fmt.Fprintf(&b, "  %-28s %8s  %3d%%\n", row.name, tokensOf(row.n), pct(row.n, c.Total))
		}
	}
	fmt.Fprintf(&b, "\n%d messages over %d turns\n", c.Messages, c.Turns)
	if len(c.ByTool) > 0 {
		b.WriteString("\nby tool:\n")
		for _, t := range c.ByTool {
			fmt.Fprintf(&b, "  %-20s %3d calls  %8s\n", t.Name, t.Calls, tokensOf(t.Tokens))
		}
	}
	if len(c.Largest) > 0 {
		b.WriteString("\nlargest results still in context:\n")
		for _, it := range c.Largest {
			fmt.Fprintf(&b, "  %8s  %s %s (message %d)\n", tokensOf(it.Tokens), it.Tool, trunc(it.Subject, 50), it.Index)
		}
	}
	return b.String()
}

func sessionReport(s *session.Session, a *agent.Agent) string {
	if s == nil {
		return "no session file (this run is not being recorded)"
	}
	var b strings.Builder
	m := s.Meta
	fmt.Fprintf(&b, "id       %s\n", m.ID)
	fmt.Fprintf(&b, "title    %s\n", orNone(m.Title))
	fmt.Fprintf(&b, "started  %s\n", m.Created.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "provider %s/%s\n", m.Provider, m.Model)
	fmt.Fprintf(&b, "cwd      %s\n", m.Cwd)
	if m.ForkOf != "" {
		fmt.Fprintf(&b, "forked   from %s\n", m.ForkOf)
	}
	fmt.Fprintf(&b, "file     %s", s.Path())
	if st, err := os.Stat(s.Path()); err == nil {
		fmt.Fprintf(&b, " (%s)", humanBytes(st.Size()))
	}
	b.WriteString("\n")
	ev := s.Events()
	live := len(session.CurrentSeqs(ev))
	fmt.Fprintf(&b, "events   %d recorded, %d messages on the current surface\n", len(ev), live)
	if len(m.Pins) > 0 {
		fmt.Fprintf(&b, "pins     %d\n", len(m.Pins))
		for _, p := range m.Pins {
			fmt.Fprintf(&b, "  - %s\n", trunc(p, 100))
		}
	}
	if a != nil {
		u := a.UsageSnapshot()
		fmt.Fprintf(&b, "usage    in %s · cache %s · out %s\n", tokensOf(u.InputTokens), tokensOf(u.CacheReadTokens), tokensOf(u.OutputTokens))
	}
	return b.String()
}

// eventsReport lists the log. Without an argument it shows the tail; a
// number shows that many, a type filters by it, and "#seq" shows one whole.
func eventsReport(s *session.Session, arg string) string {
	if s == nil {
		return "no session file"
	}
	ev := s.Events()
	if len(ev) == 0 {
		return "no events yet"
	}
	if strings.HasPrefix(arg, "#") {
		want, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if err == nil {
			for _, e := range ev {
				if e.Seq == want {
					return eventDetail(ev, e)
				}
			}
		}
		return fmt.Sprintf("no event #%s", strings.TrimPrefix(arg, "#"))
	}
	limit := 40
	filter := ""
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		limit = n
	} else if arg != "" {
		filter = strings.ToLower(arg)
	}
	hidden := session.Shadowed(ev)
	var rows []string
	for _, e := range ev {
		if filter != "" && !strings.Contains(strings.ToLower(e.Type+" "+e.Name), filter) {
			continue
		}
		rows = append(rows, fmt.Sprintf("%-5d %d/%-3d %-20s %-12s %6s %s",
			e.Seq, e.Turn, e.Step, e.Type, trunc(e.Name, 12), durMs(e.DurationMs), session.ClassifyWith(hidden, e)))
	}
	if len(rows) == 0 {
		return "no events match " + arg
	}
	head := fmt.Sprintf("%d of %d events (inspect events #<seq> for one in full)\n", min(limit, len(rows)), len(rows))
	head += fmt.Sprintf("%-5s %-5s %-20s %-12s %6s %s\n", "seq", "t/s", "type", "name", "dur", "surface")
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return head + strings.Join(rows, "\n")
}

func eventDetail(ev []session.Event, e session.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seq %d · %s · turn %d step %d · %s\n", e.Seq, e.Type, e.Turn, e.Step, session.Classify(ev, e))
	fmt.Fprintf(&b, "at %s\n", e.Time.Local().Format("15:04:05.000"))
	if e.Name != "" {
		fmt.Fprintf(&b, "name %s\n", e.Name)
	}
	if e.CallID != "" {
		fmt.Fprintf(&b, "call %s\n", e.CallID)
	}
	if e.DurationMs > 0 {
		fmt.Fprintf(&b, "duration %dms\n", e.DurationMs)
	}
	if e.Usage != nil {
		fmt.Fprintf(&b, "usage in %d · cache %d · out %d\n", e.Usage.InputTokens, e.Usage.CacheReadTokens, e.Usage.OutputTokens)
	}
	if len(e.Replaces) > 0 {
		fmt.Fprintf(&b, "replaces %v\n", e.Replaces)
	}
	if len(e.Shadowed) > 0 {
		fmt.Fprintf(&b, "shadowed %d earlier events\n", len(e.Shadowed))
	}
	if e.Arguments != "" {
		fmt.Fprintf(&b, "\narguments:\n%s\n", trunc(e.Arguments, 2000))
	}
	body := e.Output
	if body == "" && e.Message != nil {
		body = e.Message.Text()
	}
	if body != "" {
		fmt.Fprintf(&b, "\n%s\n", trunc(body, 4000))
	}
	return b.String()
}

func archiveReport(ar *agent.Archive, arg string) string {
	if ar == nil {
		return "no archive in this session"
	}
	if arg != "" {
		e, body, err := ar.Get(arg)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%s · %s %s · %s · %s\n\n%s", e.ID, e.Name, e.Subject, humanBytes(int64(e.Size)),
			e.Created.Local().Format("15:04:05"), trunc(body, 4000))
	}
	entries := ar.List()
	if len(entries) == 0 {
		return "nothing archived yet"
	}
	var b strings.Builder
	total := 0
	for _, e := range entries {
		total += e.Size
	}
	fmt.Fprintf(&b, "%d entries, %s on disk (inspect archive <id> for one)\n", len(entries), humanBytes(int64(total)))
	for _, e := range entries {
		fmt.Fprintf(&b, "  %-10s %-14s %-40s %8s %s\n", e.ID, e.Name, trunc(e.Subject, 40), humanBytes(int64(e.Size)), e.Created.Local().Format("15:04:05"))
	}
	return b.String()
}

// retrievalReport is the record of every archive lookup: what was asked
// and whether it returned anything. A recall that failed because the
// search found nothing is not the same as one that failed for any other
// reason, and this is where the difference shows.
func retrievalReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	hits, misses := 0, 0
	for _, e := range s.Events() {
		if e.Type != session.TypeRetrieval {
			continue
		}
		mark := "found"
		if e.IsError {
			mark = "nothing"
			misses++
		} else {
			hits++
		}
		rows = append(rows, fmt.Sprintf("  %-5d %-6s %-7s %-52s %s", e.Seq, e.Name, mark, trunc(e.Arguments, 52), tokensOf(e.Tokens/4)))
	}
	if len(rows) == 0 {
		return "no archive lookups yet"
	}
	head := fmt.Sprintf("%d lookups: %d found something, %d found nothing\n", hits+misses, hits, misses)
	head += fmt.Sprintf("  %-5s %-6s %-7s %-52s %s\n", "seq", "mode", "result", "query", "size")
	return head + strings.Join(rows, "\n")
}

func compactionReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	var totalIn, totalOut int
	for _, e := range s.Events() {
		if e.Type != session.TypeCompactSummary {
			continue
		}
		in, out := 0, 0
		if e.Usage != nil {
			in, out = e.Usage.InputTokens+e.Usage.CacheReadTokens, e.Usage.OutputTokens
			totalIn, totalOut = totalIn+in, totalOut+out
		}
		cost := "no summary call (the record was carried forward)"
		if in > 0 || out > 0 {
			cost = fmt.Sprintf("summary cost %s in / %s out", tokensOf(in), tokensOf(out))
		}
		rows = append(rows, fmt.Sprintf("  seq %-5d turn %-3d was ~%s, replaced %d messages · %s",
			e.Seq, e.Turn, tokensOf(e.Tokens), len(e.Shadowed), cost))
	}
	if len(rows) == 0 {
		return "the history has not been compacted in this session"
	}
	head := fmt.Sprintf("%d compactions · summaries cost %s in / %s out in total\n", len(rows), tokensOf(totalIn), tokensOf(totalOut))
	return head + strings.Join(rows, "\n")
}

// verdictsReport is the record of what each turn demonstrated. It is the
// answer to "did it really do that": the claim and the evidence side by
// side, decided from what the harness watched rather than from the answer.
func verdictsReport(s *session.Session) string {
	if s == nil {
		return "no session file"
	}
	var rows []string
	counts := map[string]int{}
	for _, e := range s.Events() {
		if e.Type != session.TypeVerdict {
			continue
		}
		counts[e.Name]++
		mark := "  "
		if e.IsError {
			mark = "! "
		}
		row := fmt.Sprintf("%sturn %-3d %-17s %s", mark, e.Turn, e.Name, e.Output)
		var o struct {
			Claimed string `json:"claimed"`
		}
		if len(e.Extra) > 0 && json.Unmarshal(e.Extra, &o) == nil && o.Claimed != "" {
			row += fmt.Sprintf("\n            the answer said: %q", o.Claimed)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "no turn has changed code yet"
	}
	var head strings.Builder
	head.WriteString("what each turn demonstrated:\n")
	for _, k := range []string{string(agent.VerdictVerified), string(agent.VerdictNotDemonstrated), string(agent.VerdictUnverified), string(agent.VerdictCheckFailed), string(agent.VerdictVacuous)} {
		if counts[k] > 0 {
			fmt.Fprintf(&head, "  %-17s %d\n", k, counts[k])
		}
	}
	head.WriteString("\n")
	return head.String() + strings.Join(rows, "\n")
}

func todosReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	return agent.RenderTodos(a.Todos())
}

func goalsReport(a *agent.Agent) string {
	if a == nil || a.Log == nil {
		return "no session log"
	}
	gs := agent.FoldGoals(a.Log.Events())
	if len(gs) == 0 {
		return "no goals in this session"
	}
	var b strings.Builder
	for _, g := range gs {
		fmt.Fprintf(&b, "  %-5s %-9s %d/%d rounds  %s\n", g.ID, g.Phase, g.Rounds, g.MaxRounds, trunc(g.Objective, 70))
		if g.Reason != "" {
			fmt.Fprintf(&b, "        %s\n", trunc(g.Reason, 80))
		}
	}
	return b.String()
}

func workingReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	if r := a.WorkingMemory(); strings.TrimSpace(r) != "" {
		return r
	}
	return "the working record is empty (nothing has been compacted yet)"
}

func toolsReport(a *agent.Agent) string {
	if a == nil || a.Tools == nil {
		return "no tools"
	}
	defs := a.Tools.Defs()
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	var b strings.Builder
	total := 0
	for _, d := range defs {
		total += len(d.Description) + len(d.InputSchema)
	}
	fmt.Fprintf(&b, "%d tools, ~%s of schema in every request\n", len(defs), tokensOf(total/4))
	for _, d := range defs {
		mutating := ""
		if t, ok := a.Tools.Get(d.Name); ok && t.Mutating() {
			mutating = "  (asks first)"
		}
		fmt.Fprintf(&b, "  %-18s %6s%s\n", d.Name, tokensOf((len(d.Description)+len(d.InputSchema))/4), mutating)
	}
	return b.String()
}

func statsReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	s := a.Stats()
	calls, errs := s.ToolCalls()
	var b strings.Builder
	fmt.Fprintf(&b, "%d turns · %d model calls · %d tool calls (%d failed) · %d compactions\n",
		s.Turns, s.ModelCalls, calls, errs, s.Compactions)
	if s.ModelTime > 0 {
		fmt.Fprintf(&b, "model time %s (%d failed calls)\n", s.ModelTime.Round(time.Millisecond), s.ModelErrors)
	}
	if len(s.Tools) > 0 {
		b.WriteString("\n  tool                calls  errors      time\n")
		for _, t := range s.Tools {
			fmt.Fprintf(&b, "  %-18s %6d  %6d  %8s\n", t.Name, t.Calls, t.Errors, t.Time.Round(time.Millisecond))
		}
	}
	return b.String()
}

func trajectoryReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	f := a.Trajectory()
	var b strings.Builder
	b.WriteString("the features the critic scores this turn on:\n")
	for _, n := range agent.FeatureNames {
		fmt.Fprintf(&b, "  %-18s %10.2f\n", n, f[n])
	}
	if p := a.Predictor; p != nil {
		fmt.Fprintf(&b, "\ncritic trained on %d runs, %.0f%% cross-validated (guessing the majority: %.0f%%)\n",
			p.Samples, p.Accuracy*100, p.Baseline*100)
	} else {
		b.WriteString("\nno trained critic is loaded, so these are recorded but not scored\n")
	}
	return b.String()
}

func configReport(cfg *config.Config, path, version, cwd string) string {
	if cfg == nil {
		return "no configuration"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "janetcode %s in %s\n", version, cwd)
	fmt.Fprintf(&b, "config file %s", path)
	if _, err := os.Stat(path); err != nil {
		b.WriteString(" (not present; defaults and the environment decide)")
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "  provider        %s\n", cfg.Provider)
	fmt.Fprintf(&b, "  model           %s\n", cfg.Model)
	fmt.Fprintf(&b, "  max tokens      %d\n", cfg.MaxTokens)
	fmt.Fprintf(&b, "  max iterations  %d\n", cfg.MaxIterations)
	fmt.Fprintf(&b, "  compact at      %s\n", compactAtText(cfg.CompactAt))
	fmt.Fprintf(&b, "  auto approve    %v\n", cfg.AutoApprove)
	fmt.Fprintf(&b, "  auto memory     %v\n", cfg.AutoMemory())
	if m := cfg.MetaFor(cfg.Model); m.ContextWindow > 0 {
		fmt.Fprintf(&b, "  context window  %s (from the model table)\n", tokensOf(m.ContextWindow))
	}
	// Where a key came from matters more than what it is.
	b.WriteString("\ncredentials:\n")
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "DASHSCOPE_API_KEY", "OPENAI_BASE_URL"} {
		if v := os.Getenv(env); v != "" {
			fmt.Fprintf(&b, "  %-18s set in the environment (%d characters)\n", env, len(v))
		}
	}
	if len(cfg.Providers) > 0 {
		b.WriteString("\nproviders configured:\n")
		names := make([]string, 0, len(cfg.Providers))
		for n := range cfg.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := cfg.Providers[n]
			key := "no key"
			if p.APIKey != "" {
				key = "key set"
			}
			fmt.Fprintf(&b, "  %-12s %-10s %s\n", n, key, p.BaseURL)
		}
	}
	return b.String()
}

func compactAtText(n int) string {
	switch {
	case n < 0:
		return "never (compaction is off)"
	case n == 0:
		return fmt.Sprintf("%s (the default)", tokensOf(agent.DefaultCompactAt))
	}
	return tokensOf(n)
}

func permissionsReport(a *agent.Agent, cfg *config.Config) string {
	if a == nil {
		return "no agent"
	}
	var b strings.Builder
	switch {
	case a.ReadOnly:
		b.WriteString("read-only: nothing that changes files or runs commands is offered\n")
	case a.AutoApprove:
		b.WriteString("auto-approve is ON: mutating tools run without asking\n")
	default:
		b.WriteString("edits and commands ask first\n")
	}
	if a.PlanMode {
		b.WriteString("plan mode: the model may look but not change\n")
	}
	rules := a.Rules
	if len(rules.Allow) == 0 && len(rules.Deny) == 0 {
		b.WriteString("\nno standing rules; every mutating call is asked about\n")
		return b.String()
	}
	if len(rules.Allow) > 0 {
		b.WriteString("\nallowed without asking:\n")
		for _, r := range rules.Allow {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	if len(rules.Deny) > 0 {
		b.WriteString("\nalways denied:\n")
		for _, r := range rules.Deny {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	return b.String()
}

func lspReport(m *lsp.Manager) string {
	if m == nil || !m.Enabled() {
		return "no language server is configured for this project"
	}
	var b strings.Builder
	b.WriteString("language servers:\n")
	for _, s := range m.Status() {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	known := m.Known()
	if len(known) == 0 {
		b.WriteString("\nno problems reported so far\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d known problems:\n", len(known))
	for i, k := range known {
		if i >= 20 {
			fmt.Fprintf(&b, "  … and %d more\n", len(known)-i)
			break
		}
		fmt.Fprintf(&b, "  %s\n", k)
	}
	return b.String()
}

func mcpReport(m *mcp.Manager) string {
	if m == nil {
		return "no MCP servers in this session"
	}
	st := m.Status()
	if len(st) == 0 {
		return "no MCP servers are configured"
	}
	var b strings.Builder
	for _, s := range st {
		state := "ready"
		switch {
		case s.Err != nil:
			state = "failed"
		case s.Starting:
			state = "starting"
		}
		fmt.Fprintf(&b, "  %-16s %-9s %d tools", s.Name, state, len(s.Tools))
		if len(s.Prompts) > 0 {
			fmt.Fprintf(&b, ", %d prompts", len(s.Prompts))
		}
		b.WriteString("\n")
		if s.Err != nil {
			fmt.Fprintf(&b, "      %s\n", trunc(s.Err.Error(), 90))
		}
		if len(s.Tools) > 0 {
			fmt.Fprintf(&b, "      %s\n", trunc(strings.Join(s.Tools, ", "), 90))
		}
	}
	return b.String()
}

func jobsReport(j *tool.Jobs) string {
	if j == nil {
		return "background jobs are not available in this session"
	}
	list := j.List()
	if len(list) == 0 {
		return "no background jobs"
	}
	var b strings.Builder
	for _, job := range list {
		state := "running"
		if job.Done {
			state = fmt.Sprintf("exited %d", job.Exit)
		}
		fmt.Fprintf(&b, "  %-4d %-12s %s\n       %s\n", job.ID, state, trunc(job.Command, 90), job.OutPath)
	}
	return b.String()
}

func subagentsReport(t *agent.Task) string {
	if t == nil {
		return "the subagent tool is not in this session"
	}
	ids := t.ChildIDs()
	if len(ids) == 0 {
		return "no subagents have run"
	}
	u := t.Usage()
	var b strings.Builder
	fmt.Fprintf(&b, "%d subagents can be continued · they have spent in %s · out %s\n",
		len(ids), tokensOf(u.InputTokens+u.CacheReadTokens), tokensOf(u.OutputTokens))
	for _, id := range ids {
		fmt.Fprintf(&b, "  %s\n", id)
	}
	return b.String()
}

func skillsReport(load func() []skill.Skill) string {
	if load == nil {
		return "skills are not loaded in this session"
	}
	sk := load()
	if len(sk) == 0 {
		return "no skills found for this project"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d skills:\n", len(sk))
	for _, s := range sk {
		fmt.Fprintf(&b, "  %-24s %s\n", s.Name, trunc(s.Description, 80))
	}
	return b.String()
}

func notesReport(m *memory.Store) string {
	if m == nil {
		return "memory is not enabled in this session"
	}
	if idx := strings.TrimSpace(m.Index()); idx != "" {
		return idx
	}
	return "nothing has been remembered for this project"
}

func repomapReport(ctx context.Context, c *repomap.Cache, query string) (string, error) {
	if c == nil {
		return "the repository map is not built in this session", nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ix, err := c.Get(rctx)
	if err != nil {
		return "", err
	}
	if ix == nil || len(ix.Files) == 0 {
		return "the repository map is empty", nil
	}
	if query == "" {
		return fmt.Sprintf("%d source files indexed (inspect repomap <query> to see how they rank for a request)", len(ix.Files)), nil
	}
	ranked := ix.Rank(query, nil)
	if len(ranked) == 0 {
		return "nothing ranks for " + query, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d files, ranked for %q:\n", len(ix.Files), query)
	for i, r := range ranked {
		if i >= 20 {
			break
		}
		why := ""
		if len(r.Why) > 0 {
			why = " — " + strings.Join(r.Why, ", ")
		}
		fmt.Fprintf(&b, "  %6.2f  %s%s\n", r.Score, r.File.Path, trunc(why, 70))
	}
	return b.String(), nil
}

func undoReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	changes := a.Changes()
	if len(changes) == 0 {
		return "no file changes to undo"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file changes recorded (newest last):\n", len(changes))
	for i, c := range changes {
		fmt.Fprintf(&b, "  %-3d turn %-3d %-10s %s\n", i+1, c.Turn, c.Tool, c.Path)
		if c.Intent != "" {
			fmt.Fprintf(&b, "      %s\n", trunc(c.Intent, 90))
		}
	}
	return b.String()
}

// Shared formatting. Reports are read by people and by the model, so they
// are plain text with aligned columns rather than JSON.

func tokensOf(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

func pct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return n * 100 / total
}

func durMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return strconv.FormatInt(ms, 10) + "ms"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fkB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// inspectOnce runs one subject and prints it, for the --inspect flag.
func inspectOnce(r *inspect.Registry, request string, out *os.File) error {
	report, err := r.Report(context.Background(), request)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, report)
	return nil
}

// unused keeps imports honest when a subsystem is compiled out.
var _ = json.Marshal
var _ = filepath.Join
var _ = llm.RoleUser
