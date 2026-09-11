// Package tui is the interactive terminal interface built on bubbletea.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// Options configures the UI.
type Options struct {
	// Notes are decisions the configuration made on the user's behalf
	// (a provider switched for one that has a key); shown once at start.
	Notes []string
	// ConfigDir holds global custom commands under commands/*.md.
	ConfigDir string
	// MCP, when set, describes the servers to start.
	MCP   *MCPOptions
	Hooks *HookOptions
	Perms *PermOptions
	// Verify carries the project's verification commands (/verify).
	Verify *VerifyOptions
	// Rules says which path-scoped rules this session has already given
	// the model, for /rules to mark them.
	Rules *rules.Injector
	// LSP reports language-server status (/lsp).
	LSP *lsp.Manager
	// Inspect answers for every part of the session; /inspect reads it.
	Inspect *inspect.Registry
	// Models resolves a model name to the backend serving it, so /model
	// can follow a name to another provider.
	Models func(string) (llm.Provider, string, error)
	// Jobs are background commands; finished ones are announced while idle.
	Jobs *tool.Jobs
	// Running is the registry of foreground commands that can be moved to
	// the background (ctrl+g). Nil disables the key.
	Running *tool.Running
	// Typed collects the hosts the user names in their own messages;
	// fetch_url reaches a private address only for one of them.
	Typed *tool.URLSet
	// AutoTitle asks the model for a short session title after the first turn.
	AutoTitle bool
	Agent     *agent.Agent
	Session   *session.Session
	Provider  string
	// Config, ConfigPath and Build enable /connect and the /model picker:
	// Build constructs a backend for a config, Update persists the choice.
	Config     *config.Config
	ConfigPath string
	Build      func(*config.Config) (llm.Provider, error)
	// Connect opens the provider dialog at start because Agent.Provider is
	// nil: nothing is configured yet.
	Connect bool
	Cwd     string
	Resumed int // messages loaded from a resumed session
	Version string
	// Theme is "dark", "light" or "" for terminal detection.
	Theme string
	// InitialPrompt is sent as soon as the UI is ready.
	InitialPrompt string
	// Memory is the project's memory store; AutoMemory extracts facts
	// after turns that changed something.
	Memory     *memory.Store
	AutoMemory bool
}

// Run starts the interactive UI and blocks until the user quits.
func Run(opts Options) error {
	m := newModel(opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	m.send = p.Send
	m.queue.send = p.Send
	m.approver.send = p.Send
	opts.Agent.Approver = m.approver
	_, err := p.Run()
	return err
}

// MCPOptions lists the servers from the config file (started right away)
// and from the project's .mcp.json (started once the user trusts it).
type MCPOptions struct {
	Manager        *mcp.Manager
	Global         map[string]mcp.ServerConfig
	Project        map[string]mcp.ServerConfig
	ProjectHash    string
	ProjectTrusted bool
	Trust          mcp.TrustStore
	// Tokens stores OAuth tokens for /mcp auth; nil disables it.
	Tokens *mcp.TokenStore
	// OpenURL opens the browser for /mcp auth; nil uses mcp.OpenBrowser.
	OpenURL func(string) error
}

// HookOptions describes the hooks in effect for /hooks.
type HookOptions struct {
	Global         hook.Config
	Project        hook.Config
	ProjectHash    string
	ProjectTrusted bool
	Trust          mcp.TrustStore
}

// extractMemory runs memory extraction in the background.
func (m *model) extractMemory() tea.Cmd {
	ag, store := m.opts.Agent, m.opts.Memory
	msgs := append([]llm.Message(nil), ag.Messages...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		xs, err := memory.Extract(ctx, ag.Provider, ag.Model, msgs, store.Entries())
		if err != nil {
			return memoryMsg{err: err}
		}
		changed, err := store.Apply(xs)
		return memoryMsg{added: changed, err: err}
	}
}

func memoryLine(e memory.Entry) string {
	scope := ""
	if e.Scope == "global" {
		scope = ", global"
	}
	return fmt.Sprintf("%s (%s%s): %s", e.Name, e.Type, scope, e.Description)
}

// memoryCommand implements /memory [query|show <name>], /remember [-g] and
// /forget.
func (m *model) memoryCommand(cmd, arg string) {
	store := m.opts.Memory
	if store == nil {
		m.transcript.errorf("memory is not available in this session")
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
			m.transcript.errorf("usage: /remember [-g] <what to keep> · -g keeps it about you, for every project")
			return
		}
		e, status, err := store.Put(memory.Entry{Body: arg, Scope: scope, Source: "user"})
		switch {
		case err != nil:
			m.transcript.errorf("remember: %v", err)
		case status == memory.Unchanged:
			m.transcript.info("already remembered as " + e.Name)
		case status == memory.Updated:
			m.transcript.info(noteMemory + "memory updated: " + memoryLine(e))
		default:
			m.transcript.info(noteMemory + "remembered: " + memoryLine(e))
		}
	case "/forget":
		if arg == "" {
			m.transcript.errorf("usage: /forget <name or number from /memory>")
			return
		}
		gone, err := store.Remove(arg)
		if err != nil {
			m.transcript.errorf("forget: %v", err)
			return
		}
		m.transcript.info("forgot " + gone.Name + ": " + gone.Description)
	default:
		entries := store.Entries()
		if len(entries) == 0 {
			m.transcript.info("nothing remembered yet (project " + store.Key + ") · /remember <fact> adds one, -g for something about you; facts are also extracted automatically after turns that change something or give feedback")
			return
		}
		var sb strings.Builder
		switch {
		case strings.HasPrefix(arg, "show "):
			e, ok := store.Get(strings.TrimSpace(strings.TrimPrefix(arg, "show ")))
			if !ok {
				m.transcript.errorf("no memory entry named %q", strings.TrimSpace(strings.TrimPrefix(arg, "show ")))
				return
			}
			fmt.Fprintf(&sb, "%s\n%s\nsource %s · used %d× · %s\n", memoryLine(e), e.Body, e.Source, e.Uses, shortPath(e.Path))
		case arg != "":
			hits := store.SearchCtx(context.Background(), arg, 8)
			if len(hits) == 0 {
				m.transcript.info("nothing remembered matches " + strconv.Quote(arg))
				return
			}
			for _, e := range hits {
				fmt.Fprintf(&sb, "%s\n    %s\n", memoryLine(e), strings.ReplaceAll(e.Body, "\n", "\n    "))
			}
		default:
			fmt.Fprintf(&sb, "memory: %d entries (%s)\n", len(entries), shortPath(store.Root))
			for i, e := range entries {
				fmt.Fprintf(&sb, "%3d. %s\n", i+1, memoryLine(e))
			}
			sb.WriteString("/memory <words> recalls, /memory show <name> prints one, /forget <name|number> removes")
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	}
}

// PermOptions describes the permission rules in effect for /permissions.
type PermOptions struct {
	Project        perm.Rules
	ProjectHash    string
	ProjectTrusted bool
	Trust          mcp.TrustStore
	// Save persists a rule to the config file.
	Save func(kind string, r perm.Rule) error
}

// permissionsCommand lists rules, adds one, or trusts the project file.
func (m *model) permissionsCommand(args []string) {
	o := m.opts.Perms
	ag := m.opts.Agent
	if len(args) >= 2 && (args[0] == "allow" || args[0] == "deny") {
		r, err := perm.Parse(strings.Join(args[1:], " "))
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		v := perm.Allowed
		if args[0] == "deny" {
			v = perm.Denied
		}
		ag.Rules.Add(v, r)
		if o != nil && o.Save != nil {
			if err := o.Save(args[0], r); err != nil {
				m.transcript.errorf("save rule: %v", err)
				return
			}
		}
		m.transcript.info("rule saved: " + args[0] + " " + r.String())
		return
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case o == nil || len(o.Project.Allow) == 0 && len(o.Project.Deny) == 0:
			m.transcript.errorf("no %s in %s", perm.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.transcript.info("project rules are already trusted")
		default:
			if err := o.Trust.Trust(m.opts.Cwd+"#permissions", o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			ag.Rules = perm.Merge(ag.Rules, o.Project)
			m.transcript.info("trusted " + perm.ProjectFile + " · its allow rules are active")
		}
		return
	}
	var sb strings.Builder
	if len(ag.Rules.Allow) == 0 && len(ag.Rules.Deny) == 0 {
		sb.WriteString("no permission rules yet\n")
	}
	for _, r := range ag.Rules.Deny {
		sb.WriteString("deny   " + r.String() + "\n")
	}
	for _, r := range ag.Rules.Allow {
		sb.WriteString("allow  " + r.String() + "\n")
	}
	if o != nil && len(o.Project.Allow) > 0 && !o.ProjectTrusted {
		sb.WriteString(perm.ProjectFile + " has allow rules that are not trusted yet; run /permissions trust to enable them\n")
	}
	sb.WriteString("/permissions allow bash(go test *) adds a rule (deny likewise); press r at a prompt to save one for that call")
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

// planCommand enters plan mode, approves the plan, or leaves the mode.
func (m *model) planCommand(arg string) tea.Cmd {
	ag := m.opts.Agent
	switch arg {
	case "approve", "go", "execute":
		if !ag.PlanMode {
			m.transcript.errorf("not in plan mode: /plan <request> writes a plan first")
			m.refresh(false)
			return nil
		}
		if m.running {
			m.transcript.errorf("wait for the plan to finish")
			m.refresh(false)
			return nil
		}
		ag.SetPlanMode(false)
		m.transcript.info("plan approved · leaving plan mode; each step is undoable with /undo")
		return m.submit("The plan is approved. Execute it now, step by step, in the order written. Verify each step (build, tests) before starting the next, and stop to report if a step fails or the plan turns out to be wrong.")
	case "off", "cancel", "exit":
		ag.SetPlanMode(false)
		m.transcript.info("left plan mode without executing")
		m.refresh(false)
		return nil
	}
	if !ag.PlanMode {
		ag.SetPlanMode(true)
		m.transcript.info("plan mode on · the model reads and searches but changes nothing; /plan approve executes the plan, /plan off leaves")
	}
	if arg == "" {
		m.refresh(false)
		return nil
	}
	return m.submit(arg)
}

// contextCommand shows what fills the context window.
func (m *model) contextCommand() {
	r := m.opts.Agent.Context()
	var sb strings.Builder
	pct := 0
	if r.CompactAt > 0 {
		pct = r.Total * 100 / r.CompactAt
	}
	fmt.Fprintf(&sb, "context ~%s tokens · compaction at %s (%d%%) · %d messages, %d turns\n", shortTokens(r.Total), shortTokens(r.CompactAt), pct, r.Messages, r.Turns)
	fmt.Fprintf(&sb, "  system prompt %6s   memory+skill index %6s   pins %6s (%d)\n", shortTokens(r.System), shortTokens(r.Extra), shortTokens(r.Pins), len(m.opts.Agent.Pins))
	fmt.Fprintf(&sb, "  tools         %6s   (%d definitions", shortTokens(r.Tools), r.ToolCount)
	if r.Deferred > 0 {
		fmt.Fprintf(&sb, ", %d deferred", r.Deferred)
	}
	sb.WriteString(")\n")
	fmt.Fprintf(&sb, "  user text     %6s   assistant text %6s   thinking %6s\n", shortTokens(r.UserText), shortTokens(r.AssistantText), shortTokens(r.Thinking))
	fmt.Fprintf(&sb, "  tool calls    %6s   tool results   %6s", shortTokens(r.ToolCalls), shortTokens(r.ToolResults))
	if r.Images > 0 {
		fmt.Fprintf(&sb, "   images %d (~%s)", r.Images, shortTokens(r.Images*1600))
	}
	sb.WriteString("\n")
	if len(r.ByTool) > 0 {
		sb.WriteString("by tool:")
		for i, t := range r.ByTool {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&sb, " %s %s (%d)", t.Name, shortTokens(t.Tokens), t.Calls)
		}
		sb.WriteString("\n")
	}
	if len(r.Largest) > 0 {
		sb.WriteString("largest tool results:\n")
		for _, it := range r.Largest {
			subj := it.Subject
			if strings.HasPrefix(subj, "/") {
				subj = m.relPath(subj)
			}
			if subj != "" {
				subj = " " + trunc(subj, 50)
			}
			fmt.Fprintf(&sb, "  %6s  %s%s (message %d)\n", shortTokens(it.Tokens), it.Tool, subj, it.Index)
		}
	}
	if false {
		// The surface meter is a diagnostic; /inspect has it.
		meter := session.Measure(m.sessionEvents(), nil)
		fmt.Fprintf(&sb, "surface meter · log rev %d · %s across %d nodes\n", meter.LogRevision, shortTokens(meter.Surface), len(meter.Nodes))
		sort.Slice(meter.Nodes, func(i, j int) bool { return meter.Nodes[i].Tokens > meter.Nodes[j].Tokens })
		for i, n := range meter.Nodes {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&sb, "  seq %d  %s  %s\n", n.Seq, n.Kind, shortTokens(n.Tokens))
		}
	}
	sb.WriteString("/compact summarises the history, /pin keeps a note through it")
	m.transcript.info(sb.String())
}

// sessionEvents is a snapshot of the session log, or nil without a session.
func (m *model) sessionEvents() []session.Event {
	if m.opts.Session == nil {
		return nil
	}
	return m.opts.Session.Events()
}

// inspectCommand reports one part of the session state. Everything the
// harness knows about itself is reachable from here, so a surprising turn
// can be looked into rather than guessed at.
func (m *model) inspectCommand(arg string) {
	if m.opts.Inspect == nil {
		m.transcript.errorf("nothing is registered for inspection in this session")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := m.opts.Inspect.Report(ctx, arg)
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.transcript.info(out)
}

func (m *model) trajectoryCommand(args []string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	ev := m.sessionEvents()
	if len(args) > 0 && args[0] == "grep" {
		q := strings.Join(args[1:], " ")
		hits := session.QueryEvents(ev, session.QueryFilter{Text: q})
		if len(hits) == 0 {
			m.transcript.info("no events matching " + q)
			return
		}
		var sb strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&sb, "#%d %s %s %s\n  %s\n", h.Event.Seq, h.Kind, h.Event.Type, h.Event.Name, h.Snippet)
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return
	}
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err == nil {
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
				if len(body) > 4000 {
					body = body[:4000] + "…"
				}
				if body != "" {
					sb.WriteString(body)
				}
				m.transcript.info(sb.String())
				return
			}
			m.transcript.errorf("no event seq %d", n)
			return
		}
	}
	if len(ev) == 0 {
		m.transcript.info("no trajectory events yet: no turn has run")
		return
	}
	hidden := session.Shadowed(ev)
	var sb strings.Builder
	fmt.Fprintf(&sb, "seq  turn/step  type                 name        dur    surface\n")
	for _, e := range ev {
		k := session.ClassifyWith(hidden, e)
		fmt.Fprintf(&sb, "%-4d %d/%-6d %-20s %-11s %7s %s\n", e.Seq, e.Turn, e.Step, e.Type, trunc(e.Name, 11), shortDuration(time.Duration(e.DurationMs)*time.Millisecond), k)
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

func (m *model) feedbackCommand(args []string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	if len(args) == 0 {
		m.transcript.errorf("usage: /feedback up|down [note]")
		return
	}
	r := session.RateUp
	if args[0] == "down" || args[0] == "-" {
		r = session.RateDown
	} else if args[0] != "up" && args[0] != "+" {
		m.transcript.errorf("usage: /feedback up|down [note]")
		return
	}
	note := strings.Join(args[1:], " ")
	if err := m.opts.Session.Rate(0, r, note); err != nil {
		m.transcript.errorf("feedback: %v", err)
		return
	}
	m.transcript.info("recorded " + string(r) + " on last assistant message")
}

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
		var sb strings.Builder
		for _, g := range gs {
			fmt.Fprintf(&sb, "%s  %-8s  %d/%d  %s\n", g.ID, g.Phase, g.Rounds, g.MaxRounds, g.Objective)
			if g.Reason != "" {
				fmt.Fprintf(&sb, "    %s\n", g.Reason)
			}
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return nil
	case arg == "pause" || arg == "resume" || arg == "done":
		if ag.Log == nil {
			m.transcript.errorf("no session log")
			return nil
		}
		ev := ag.Log.Events()
		// resume acts on the goal that was paused; pause and done on the
		// active one. Either falls back to the most recent goal.
		g := agent.ActiveGoal(ev)
		if arg == "resume" {
			g = agent.LastGoal(ev, agent.GoalPaused)
		}
		if g == nil {
			g = agent.LastGoal(ev, "")
		}
		if g == nil {
			m.transcript.errorf("no goal")
			return nil
		}
		phase := agent.GoalPaused
		if arg == "resume" {
			phase = agent.GoalActive
		}
		if arg == "done" {
			phase = agent.GoalComplete
		}
		ag.SetGoalPhase(g.ID, phase, "")
		m.transcript.info("goal " + g.ID + " " + phase)
		m.refresh(false)
		if phase == agent.GoalActive {
			return m.startGoalRound()
		}
		return nil
	default:
		id := ag.CreateGoal(arg, 0)
		m.transcript.info("goal " + id + " active: " + arg)
		return m.startGoalRound()
	}
}

// startGoalRound begins the active goal's next round now when the model
// is idle. Setting a goal and seeing nothing happen was the alternative:
// the loop only advanced when a turn ended, and there was no turn.
func (m *model) startGoalRound() tea.Cmd {
	if m.running {
		m.transcript.info("the goal continues when this turn ends")
		return nil
	}
	cont := m.opts.Agent.GoalContinuation()
	if cont == "" {
		return nil
	}
	return m.submit(cont)
}

func (m *model) agentsCommand() {
	ag := m.opts.Agent
	if ag == nil || ag.Tools == nil {
		m.transcript.info("no tools")
		return
	}
	t, ok := ag.Tools.Get("task")
	if !ok {
		m.transcript.info("no task tool")
		return
	}
	task, ok := t.(*agent.Task)
	if !ok {
		m.transcript.info("task tool is not continuable")
		return
	}
	ids := task.ChildIDs()
	if len(ids) == 0 {
		m.transcript.info("no subagents yet; task continue=<id> reuses one")
		return
	}
	m.transcript.info("subagents: " + strings.Join(ids, ", "))
}

// pinCommand adds a pin or lists them.
func (m *model) pinCommand(arg string) {
	ag := m.opts.Agent
	if arg == "" {
		if len(ag.Pins) == 0 {
			m.transcript.info("no pins: /pin <note> or /pin @path keeps something in the system prompt for the whole session")
			return
		}
		var sb strings.Builder
		for i, p := range ag.Pins {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, trunc(strings.ReplaceAll(p, "\n", " "), 120))
		}
		sb.WriteString("/unpin <n> removes one")
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return
	}
	if strings.HasPrefix(arg, "@") && !strings.ContainsAny(arg, " \t") {
		rel := strings.TrimPrefix(arg, "@")
		data, err := os.ReadFile(filepath.Join(m.opts.Cwd, rel))
		if err != nil {
			m.transcript.errorf("pin: %v", err)
			return
		}
		const maxPin = 16 * 1024
		body := string(data)
		if len(body) > maxPin {
			body = body[:maxPin] + "\n… (truncated)"
		}
		arg = "File " + rel + " as of pinning:\n" + body
	}
	ag.Pin(arg)
	m.savePins()
	m.transcript.info(fmt.Sprintf("pinned (%d): %s", len(ag.Pins), trunc(strings.ReplaceAll(arg, "\n", " "), 100)))
}

func (m *model) unpinCommand(args []string) {
	if len(args) != 1 {
		m.transcript.errorf("usage: /unpin <n>")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || !m.opts.Agent.Unpin(n) {
		m.transcript.errorf("no pin %s; /pin lists them", args[0])
		return
	}
	m.savePins()
	m.transcript.info(fmt.Sprintf("unpinned %d", n))
}

func (m *model) savePins() {
	if m.opts.Session == nil {
		return
	}
	if err := m.opts.Session.SavePins(m.opts.Agent.Pins); err != nil {
		m.transcript.errorf("could not save pins: %v", err)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// titleMsg carries a generated session title.
type titleMsg struct {
	gen   int
	title string
}

// autoTitle asks the model for a short title in the background; on any
// failure the first user message provides one.
func (m *model) autoTitle() tea.Cmd {
	m.titleGen++
	gen := m.titleGen
	ag := m.opts.Agent
	msgs := append([]llm.Message(nil), ag.Messages...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		title, err := session.GenerateTitle(ctx, ag.Provider, ag.Model, msgs)
		if err != nil || title == "" {
			title = session.AutoTitle(msgs)
		}
		return titleMsg{gen: gen, title: title}
	}
}

// forkSession branches the conversation into a new session.
func (m *model) forkSession(title string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session to fork")
		return
	}
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before forking")
		return
	}
	ns, err := session.Fork(dirOf(m.opts.Session), m.opts.Session, title)
	if err != nil {
		m.transcript.errorf("fork: %v", err)
		return
	}
	old := m.opts.Session.Label()
	m.opts.Session = ns
	m.opts.Agent.SetLog(ns) // the fork owns the log from here on
	m.opts.Agent.UndoPath = ns.UndoPath()
	m.opts.Agent.SaveUndo()
	m.transcript.info(fmt.Sprintf("forked %s → %s (%s) · this session continues from here, the original is unchanged · /resume lists both", old, ns.Meta.ID, ns.Meta.Title))
}

// setTitle names the session.
func (m *model) setTitle(title string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	if title == "" {
		m.transcript.info("title: " + m.opts.Session.Label() + " · /title <text> renames")
		return
	}
	m.titleGen++ // a pending generated title must not overwrite this
	if err := m.opts.Session.SetTitle(title); err != nil {
		m.transcript.errorf("save title: %v", err)
		return
	}
	m.transcript.info("titled: " + title)
}

// searchSessions finds earlier conversations mentioning the words.
func (m *model) searchSessions(query string) {
	if query == "" {
		m.transcript.errorf("usage: /history <words>")
		return
	}
	hits, err := session.Search(dirOf(m.opts.Session), "", query, 15)
	if err != nil {
		m.transcript.errorf("search: %v", err)
		return
	}
	if len(hits) == 0 {
		m.transcript.info("no session mentions " + query)
		return
	}
	var sb strings.Builder
	for _, h := range hits {
		where := ""
		if h.Info.Meta.Cwd != m.opts.Cwd {
			where = " in " + shortPath(h.Info.Meta.Cwd)
		}
		fmt.Fprintf(&sb, "  %s  %s%s\n      %s: %s\n", h.Info.Meta.ID, h.Info.Label(), where, h.Role, h.Snippet)
	}
	sb.WriteString("/resume <id> reopens one (sessions from other directories open there when you cd)")
	m.transcript.info(sb.String())
}

// jobsCommand lists background jobs or kills one.
func (m *model) jobsCommand(args []string) {
	if len(args) == 2 && args[0] == "kill" {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			m.transcript.errorf("usage: /jobs kill <n>")
			return
		}
		if err := m.opts.Jobs.Kill(n); err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		m.transcript.info(fmt.Sprintf("killed job %d", n))
		return
	}
	jobs := m.opts.Jobs.List()
	if len(jobs) == 0 {
		m.transcript.info("no background jobs; the model starts one with bash background:true (a dev server, a long build)")
		return
	}
	var sb strings.Builder
	for _, j := range jobs {
		sb.WriteString(j.Summary() + "\n  output: " + j.OutPath + "\n")
	}
	sb.WriteString("/jobs kill <n> stops one; all jobs stop when janetcode exits")
	m.transcript.info(sb.String())
}

// lspCommand shows language-server status and known problems.
func (m *model) lspCommand() {
	d := m.opts.LSP
	if d == nil || !d.Enabled() {
		m.transcript.info("diagnostics are off: no language server configured (gopls, pyright-langserver, typescript-language-server, rust-analyzer and clangd are used when on PATH; see \"lsp\" in the config)")
		return
	}
	var sb strings.Builder
	for _, l := range d.Status() {
		sb.WriteString(l + "\n")
	}
	known := d.Known()
	if len(known) == 0 {
		sb.WriteString("no known problems")
	} else {
		sb.WriteString(fmt.Sprintf("%d problems:\n", len(known)))
		if len(known) > 30 {
			known = append(known[:30], "…")
		}
		sb.WriteString(strings.Join(known, "\n"))
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

// hooksCommand lists hooks or trusts the project's hook file.
func (m *model) hooksCommand(args []string) {
	o := m.opts.Hooks
	if o == nil {
		m.transcript.info("hooks are not configured: add them under \"hooks\" in the config file or in " + hook.ProjectFile)
		return
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case o.Project.Len() == 0:
			m.transcript.errorf("no %s in %s", hook.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.transcript.info("project hooks are already trusted")
		default:
			if err := o.Trust.Trust(m.opts.Cwd+"#hooks", o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			if m.opts.Agent.Hooks == nil {
				m.opts.Agent.Hooks = &hook.Runner{Cwd: m.opts.Cwd}
			}
			cfg, notes := hook.MergeScoped(o.Global, o.Project)
			m.opts.Agent.Hooks.Config = cfg
			for _, n := range notes {
				m.transcript.info(noteNotice + "hooks: " + n)
			}
			m.transcript.info("trusted " + hook.ProjectFile + " · its hooks are active")
		}
		return
	}
	var sb strings.Builder
	n := 0
	for _, src := range []struct {
		label string
		cfg   hook.Config
	}{{"config file", o.Global}, {hook.ProjectFile, o.Project}} {
		for _, ev := range hook.Events {
			for _, h := range src.cfg.For(ev) {
				n++
				match := h.Match
				if match == "" {
					match = "*"
				}
				fmt.Fprintf(&sb, "%-12s %-14s %s  (%s)\n", ev, match, h.Command, src.label)
			}
		}
	}
	if n == 0 {
		m.transcript.info("no hooks configured: add them under \"hooks\" in the config file or in " + hook.ProjectFile)
		return
	}
	if o.Project.Len() > 0 && !o.ProjectTrusted {
		sb.WriteString(hook.ProjectFile + " is present but not trusted; run /hooks trust to enable it\n")
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

// mcpAuthMsg carries progress of /mcp auth; done marks the end.
type mcpAuthMsg struct {
	name string
	text string
	done bool
	err  error
}

// mcpDoneMsg reports a server that came up or failed.
type mcpDoneMsg struct {
	name  string
	tools int
	err   error
}

// mcpPromptMsg carries a rendered MCP prompt to submit.
type mcpPromptMsg struct {
	command string
	text    string
	err     error
}

// --- messages from the agent goroutine ---

type agentEventMsg struct{ ev agent.Event }

// memoryMsg reports what automatic extraction remembered.
type memoryMsg struct {
	added []memory.Entry
	err   error
}
type flushMsg struct{}
type agentDoneMsg struct {
	err error
}
type permissionMsg struct {
	call  agent.ToolCall
	reply chan verdict
}

// verdict is the user's answer to a permission prompt.
type verdict struct {
	d      agent.Decision
	reason string
}
type tickMsg time.Time

// shellDoneMsg carries the result of a "!command" the user ran directly.
type shellDoneMsg struct {
	call   agent.ToolCall
	output string
	err    error
	dur    time.Duration
}

// eventQueue coalesces agent events so that one slow render never throttles
// the model stream: the agent goroutine appends without blocking and the UI
// drains everything that accumulated on its next wake-up.
type eventQueue struct {
	mu        sync.Mutex
	events    []agent.Event
	scheduled bool
	send      func(tea.Msg)
}

func (q *eventQueue) push(e agent.Event) {
	q.mu.Lock()
	q.events = append(q.events, e)
	wake := !q.scheduled
	q.scheduled = true
	q.mu.Unlock()
	if wake {
		go q.send(flushMsg{})
	}
}

func (q *eventQueue) drain() []agent.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	evs := q.events
	q.events = nil
	q.scheduled = false
	return evs
}

// tuiApprover bridges agent permission requests into the UI loop.
type tuiApprover struct{ send func(tea.Msg) }

func (a *tuiApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

func (a *tuiApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	reply := make(chan verdict, 1)
	a.send(permissionMsg{call: call, reply: reply})
	select {
	case v := <-reply:
		return v.d, v.reason, nil
	case <-ctx.Done():
		return agent.Deny, "", ctx.Err()
	}
}

// --- slash commands ---

// availability says when a command can run. Resolving an idle-only
// command while a turn is running returns a blocked message instead of
// each command checking m.running for itself and saying it differently.
type availability int

const (
	availAlways availability = iota
	availIdle                // needs the turn to finish first
)

type command struct {
	name, args, help string
	avail            availability
	// argsFor lists the completions for this command's argument, when
	// they can be enumerated.
	argsFor func(m *model, partial string) []command
}

var commands = []command{
	{name: "/help", args: "", help: "keys and commands"},
	{name: "/compact", args: "", help: "summarise the conversation to free context"},
	{name: "/clear", args: "", help: "start a new session"},
	{name: "/resume", args: "[n|id]", help: "reopen an earlier session here: alone opens a searchable list, or name one"},
	{name: "/open", args: "<path[:line]>", help: "show a file inline, centred on a line"},
	{name: "/diff", args: "[path|revert path:n]", help: "changes against HEAD; a file's hunks; revert one hunk"},
	{name: "/fork", args: "[title]", help: "branch this conversation into a new session and continue there"},
	{name: "/title", args: "<text>", help: "name this session (titles are generated after the first turn)"},
	{name: "/history", args: "<words>", help: "find earlier sessions in any directory whose conversation mentions the words"},
	{name: "/find", args: "<text>", help: "jump to the next line matching text; repeat to continue"},
	{name: "/stats", args: "", help: "session statistics: turns, model calls, tool calls per tool, tokens, cost"},
	{name: "/undo", args: "[preview|n]", help: "revert everything the last turn changed (git snapshot; file copies outside git); preview only lists it; n reverts one change from /changes"},
	{name: "/changes", args: "[n]", help: "this session's file changes grouped by turn and intent; n shows one change's hunks"},
	{name: "/blame", args: "<path>", help: "which change wrote each line of a file changed this session"},
	{name: "/redo", args: "", help: "reapply what the last /undo reverted (until the next change)"},
	{name: "/mcp", args: "[trust|auth <server>|logout <server>]", help: "MCP servers and their tools; trust starts the project's .mcp.json servers; auth logs in to a remote server with OAuth"},
	{name: "/hooks", args: "[trust]", help: "hooks in effect; trust enables the project's .janetcode/hooks.json"},
	{name: "/permissions", args: "[allow|deny <rule>|trust]", help: "permission rules; add one, or trust the project's .janetcode/permissions.json"},
	{name: "/verify", args: "[set <cmd>|mutate <test cmd>|clear|trust]", help: "run the project's checks now; set adds one to .janetcode/verify.json; mutate requires tests to catch mutations of changed lines"},
	{name: "/lsp", args: "", help: "language servers and the diagnostics they reported"},
	{name: "/jobs", args: "[kill <n>]", help: "background commands started with bash background:true"},
	{name: "/context", args: "", help: "what fills the context window: prompt, tools, messages, largest tool results"},
	{name: "/todos", args: "", help: "the plan the model is working from"},
	{name: "/inspect", args: "[subject]", help: "look at any part of the session: context, events, archive, retrieval, config, tools, lsp, mcp, jobs, repomap… (no subject lists them)"},
	{name: "/reload", args: "", help: "rebuild the memory and skill indexes in the prompt now instead of at compaction"},
	{name: "/pin", args: "[note or @file]", help: "keep a note in the system prompt for the whole session; alone lists pins"},
	{name: "/unpin", args: "<n>", help: "remove a pin"},
	{name: "/plan", args: "[request|approve|off]", help: "plan mode: the model explores and writes a plan; approve executes it"},
	{name: "/trajectory", args: "[n|grep <words>]", help: "event ledger of this session (turn/step/tool/compaction); n shows one record"},
	{name: "/feedback", args: "up|down [note]", help: "rate the last assistant message (not sent to the model)"},
	{name: "/goal", args: "[objective|pause|resume|done]", help: "set a goal and start on it now; the model keeps going round after round until it is complete or blocked"},
	{name: "/agents", args: "", help: "list continuable subagents started with the task tool"},
	{name: "/readonly", args: "", help: "toggle read-only mode (no edits or commands)"},
	{name: "/memory", args: "", help: "what janetcode remembers about this project"},
	{name: "/skills", args: "", help: "skill files available to the model"},
	{name: "/rules", args: "", help: "path-scoped project rules: what brings each in, and which have been sent this session"},
	{name: "/remember", args: "<fact>", help: "add a fact to the project memory"},
	{name: "/forget", args: "<n>", help: "remove memory entry n"},
	{name: "/yolo", args: "", help: "toggle auto-approve for tools"},
	{name: "/edits", args: "", help: "toggle auto-accept edits: file changes go through, commands still ask"},
	{name: "/settle", args: "", help: "mark this session done with (or active again); settled sessions list last in /resume"},
	{name: "/connect", args: "", help: "connect to a provider (preset or custom API) and pick a model"},
	{name: "/model", args: "[name]", help: "pick a model from a list, or set one by name"},
	{name: "/usage", args: "", help: "tokens and cost for this session"},
	{name: "/export", args: "[file.md]", help: "write this session as a Markdown transcript"},
	{name: "/verbose", args: "", help: "toggle expanded tool output for the whole transcript (ctrl+o expands one turn)"},
	{name: "/quit", args: "", help: "exit"},
}

// idleOnly names the commands that cannot run while a turn is: they swap
// the conversation or the backend under the turn's feet. Resolving one
// mid-turn is blocked with one message, in one place.
var idleOnly = []string{"/compact", "/clear", "/new", "/resume", "/sessions", "/fork", "/connect", "/model", "/undo", "/redo"}

// argCompleters lists the commands whose argument can be enumerated.
var argCompleters = map[string]func(m *model, partial string) []command{
	"/resume": (*model).completeSession,
	"/model":  (*model).completeModel,
	"/forget": (*model).completeMemory,
	"/undo":   (*model).completeUndo,
	"/open":   (*model).completeFile,
	"/diff":   (*model).completeFile,
}

func init() {
	idle := map[string]bool{}
	for _, n := range idleOnly {
		idle[n] = true
	}
	for i := range commands {
		c := &commands[i]
		if idle[c.name] {
			c.avail = availIdle
		}
		c.argsFor = argCompleters[c.name]
	}
}

// lookup finds a built-in command by name.
func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// --- model ---

type model struct {
	opts Options
	// quitArmedAt is when ctrl+c was last pressed while idle.
	quitArmedAt time.Time
	// pendingClosedAt is when the last permission prompt closed.
	pendingClosedAt time.Time
	approver        *tuiApprover
	// send delivers messages from background goroutines into the loop.
	send func(tea.Msg)
	// titleGen invalidates generated titles once the user set one.
	titleGen int

	transcript *transcript
	queue      *eventQueue
	viewport   viewport.Model
	input      textarea.Model
	spinner    spinner.Model

	width, height int
	ready         bool
	verbose       bool
	// pinned is true while the transcript follows new output. The user
	// scrolling away from the bottom clears it; reaching the bottom again,
	// sending a prompt or starting a new session sets it. Resizing the
	// window never changes it, so a resize mid-stream does not yank a
	// reader who had scrolled up back to the tail.
	pinned      bool
	lastContent string // last transcript pushed into the viewport

	// pastes holds the text behind each "[paste #N +M lines]" chip in the
	// input, in the order they were pasted.
	pastes []string
	// history of submitted prompts for up/down recall, loaded from and
	// appended to a file per working directory (see history.go).
	history  []histEntry
	histFile string
	// lastUserText is the prompt of the running or last turn.
	lastUserText string
	histIdx      int    // == len(history) when not browsing
	draft        string // input saved while browsing history

	running        bool
	cancelling     bool
	compacting     bool
	cancel         context.CancelFunc
	startedAt      time.Time
	phase          string // what the agent is doing right now
	pending        *permissionMsg
	denying        bool // the input box is collecting a reason for a denial
	status         string
	lastTurn       string                   // usage summary of the last turn
	lastVerdict    string                   // the last turn's verdict line
	ticks          int                      // tick count, for slow periodic checks
	lastUsage      llm.Usage                // the last turn's usage
	verifyTipShown bool                     // the one-time hint about verification gates
	queued         []string                 // prompts typed while a turn was running
	steering       []string                 // notes handed to the running turn (ctrl+s)
	custom         map[string]prompt.Custom // user-defined slash commands
	clipboard      func() ([]byte, error)   // reads a PNG from the clipboard; swapped in tests
	compIdx        int                      // highlighted row of the completion menu
	compArgs       bool                     // the menu is offering arguments, not commands
	compFor        string                   // input value the highlight belongs to
	lastFind       string                   // last /find query, for repeats
	lastFindAt     int                      // line of the last hit, so repeats advance past it
	picker         *picker                  // open searchable list
	connect        *connect                 // open provider/model dialog
	connectGen     int
	quitting       bool
	branch         string
	// term writes the escape sequences that let the terminal take part:
	// OSC 133 zones, desktop notifications, the window title.
	term *termIntegration
}

const (
	minInputLines = 1
	maxInputLines = 6

	defaultPlaceholder = "Ask for a change, a fix, or an explanation… · / for commands"
)

func newModel(opts Options) *model {
	ta := textarea.New()
	ta.Placeholder = defaultPlaceholder
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = maxInputLines
	ta.SetHeight(minInputLines)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleAccent))

	applyColorProfile(os.Getenv)
	style := detectTheme(opts.Theme)
	m := &model{
		opts:       opts,
		pinned:     true,
		approver:   &tuiApprover{},
		transcript: newTranscriptStyled(style),
		queue:      &eventQueue{},
		input:      ta,
		spinner:    sp,
		term:       newTermIntegration(),
	}
	if b, ok := prompt.GitBranch(opts.Cwd); ok {
		m.branch = b
		if b == "" {
			m.branch = "git"
		}
	}
	m.clipboard = readClipboardImage
	if opts.Session != nil {
		m.histFile = histPath(dirOf(opts.Session), opts.Cwd)
		m.history = loadPromptHistory(m.histFile)
		m.histIdx = len(m.history)
	}
	m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(opts.Cwd, opts.ConfigDir) {
		m.custom["/"+c.Name] = c
	}
	if opts.Resumed > 0 {
		m.transcript.banner(m.welcome(resumedLine(opts.Session.Meta.ID, opts.Resumed)))
		m.transcript.loadHistory(opts.Agent.Messages)
	} else {
		m.transcript.banner(m.welcome(""))
	}
	return m
}

// detectTheme decides which palette the session uses. An explicit theme
// from the config wins; otherwise the terminal is asked once, here, before
// the program starts, because the query talks to the terminal and must not
// race with key input later.
//
// When the terminal does not answer — a multiplexer that swallows OSC 11,
// a CI pty, a pipe — we assume dark: most developer terminals are, and the
// dark palette on a light terminal is merely pale where the light palette
// on a dark terminal is unreadable.
//
// The answer is then pinned on lipgloss, so the adaptive colours in
// styles.go and the glamour style in mdstyle.go can never disagree: with
// "theme": "light" configured on a dark terminal the transcript used to
// render our palette dark and the model's markdown light.
func detectTheme(theme string) string {
	if theme != "dark" && theme != "light" {
		theme = "dark"
		if !lipgloss.HasDarkBackground() {
			theme = "light"
		}
	}
	lipgloss.SetHasDarkBackground(theme == "dark")
	return theme
}

// welcome is the opening banner. A resumed conversation says so on its
// first line instead of announcing the version: the two used to stack, a
// banner saying hello with an information line under it explaining that
// the conversation had in fact been going for a while.
func (m *model) welcome(resumed string) string {
	var sb strings.Builder
	switch {
	case resumed != "":
		fmt.Fprintf(&sb, "janetcode %s · %s · ", m.opts.Version, resumed)
	case m.opts.Agent.Provider == nil:
		fmt.Fprintf(&sb, "janetcode %s · no provider configured yet: pick one below, or set ANTHROPIC_API_KEY / OPENAI_API_KEY\n", m.opts.Version)
	default:
		fmt.Fprintf(&sb, "janetcode %s · ", m.opts.Version)
	}
	fmt.Fprintf(&sb, "working in %s", fitPath(shortPath(m.opts.Cwd), 60))
	if m.branch != "" {
		fmt.Fprintf(&sb, " on %s", m.branch)
	}
	sb.WriteString("\n")
	switch {
	case m.opts.Agent.AutoApprove:
		sb.WriteString("auto-approve is on: tools run without asking")
	case m.opts.Agent.AutoEdits:
		sb.WriteString("edits go through, commands ask (/edits)")
	default:
		sb.WriteString("edits and commands ask first · y allow · a always · n deny")
	}
	sb.WriteString(" · esc cancels · /help\n")
	sb.WriteString("@file attaches · !cmd runs and shares the output · /undo reverts a bad change")
	return sb.String()
}

// resumedLine names a reopened session for the banner.
func resumedLine(id string, messages int) string {
	return fmt.Sprintf("resumed session %s · %s", id, plural(messages, "message"))
}

// startMCP launches the configured servers in the background.
func (m *model) startMCP() {
	o := m.opts.MCP
	if o == nil || o.Manager == nil {
		return
	}
	start := func(servers map[string]mcp.ServerConfig) {
		for name, cfg := range servers {
			o.Manager.Start(context.Background(), name, cfg, func(name string, c *mcp.Client, err error) {
				n := 0
				if c != nil {
					n = len(c.Tools())
				}
				m.send(mcpDoneMsg{name: name, tools: n, err: err})
			})
		}
	}
	start(o.Global)
	if o.ProjectTrusted {
		start(o.Project)
	} else if len(o.Project) > 0 {
		names := make([]string, 0, len(o.Project))
		for n := range o.Project {
			names = append(names, sanitize(n))
		}
		sort.Strings(names)
		m.transcript.info(fmt.Sprintf("%s%s lists MCP servers (%s) that have not been trusted here · they would run commands on your machine: inspect the file, then /mcp trust starts them", noteNotice, mcp.ProjectFile, strings.Join(names, ", ")))
	}
	if n := len(o.Global) + len(o.Project); n > 0 && (o.ProjectTrusted || len(o.Global) > 0) {
		m.status = "starting MCP servers…"
	}
	m.refresh(false)
}

func (m *model) Init() tea.Cmd {
	for _, n := range m.opts.Notes {
		m.transcript.info(n)
	}
	m.startMCP()
	return tea.Batch(textarea.Blink, m.spinner.Tick, tick(), tea.SetWindowTitle(m.windowTitle()))
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		first := !m.ready
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refresh(true)
		if first && m.opts.Connect {
			m.opts.Connect = false
			return m, m.openConnect(stepPick)
		}
		if first && m.opts.InitialPrompt != "" {
			text := m.opts.InitialPrompt
			m.opts.InitialPrompt = ""
			m.remember(text)
			return m, m.submit(text)
		}
		return m, nil

	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case modelsMsg:
		m.handleModels(msg)
		return m, nil

	case tea.FocusMsg:
		m.term.focused = true
		return m, nil

	case tea.BlurMsg:
		m.term.focused = false
		return m, nil

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if m.clickTranscript(msg.Y) {
				return m, nil
			}
		}
		return m, m.scroll(msg)

	case tickMsg:
		// Keeps elapsed timers and throttled live markdown moving even when
		// no event arrives.
		m.ticks++
		if m.ticks%10 == 0 && !m.running {
			// Someone may have checked out another branch in this tree
			// (or in the session's worktree) from outside; follow it.
			if b, ok := prompt.GitBranch(m.opts.Cwd); ok && b != "" && b != m.branch {
				m.transcript.info(noteNotice + "branch changed: " + m.branch + " → " + b)
				m.branch = b
				m.refresh(false)
			}
		}
		if m.running {
			m.refresh(false)
		} else if notes := m.opts.Jobs.Finished(); len(notes) > 0 {
			for _, n := range notes {
				m.transcript.info(noteJob + strings.SplitN(n, "\n", 2)[0])
				m.opts.Agent.AddHarnessContext("[background job update]\n" + n)
			}
			m.refresh(false)
		}
		return m, tick()

	case spinner.TickMsg:
		if m.running || (m.connect != nil && m.connect.loading) {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
			m.refresh(false)
		}

	case agentEventMsg:
		m.applyEvent(msg.ev)
		m.refresh(false)
		return m, nil

	case flushMsg:
		m.drainEvents()
		m.refresh(false)
		return m, nil

	case permissionMsg:
		m.drainEvents() // the tool entry must exist before the prompt shows
		m.pending = &msg
		m.phase = "waiting for your approval"
		m.term.announce("janetcode", "approval needed: "+msg.call.Name)
		m.refresh(false)
		return m, nil

	case verifyDoneMsg:
		m.finishVerify(msg)
		m.refresh(false)
		return m, nil

	case mcpPromptMsg:
		m.running = false
		m.phase = ""
		if msg.err != nil {
			m.transcript.errorf("%s: %v", msg.command, msg.err)
			m.refresh(false)
			return m, nil
		}
		return m, m.submit(msg.text)

	case mcpAuthMsg:
		switch {
		case !msg.done:
			m.transcript.info(noteNotice + "mcp " + msg.name + ": " + msg.text)
		case msg.err != nil:
			m.transcript.errorf("mcp %s: authorization failed: %v", msg.name, msg.err)
		default:
			m.transcript.info(noteNotice + "mcp " + msg.name + ": authorized · restarting the server")
			m.restartMCP(msg.name)
		}
		m.refresh(false)
		return m, nil

	case mcpDoneMsg:
		if msg.err != nil {
			m.transcript.errorf("mcp %s: %s", msg.name, strings.TrimPrefix(msg.err.Error(), "mcp "+msg.name+": "))
		} else {
			line := fmt.Sprintf("mcp %s ready · %d tools", msg.name, msg.tools)
			if n := len(m.mcpPrompts(msg.name)); n > 0 {
				line += fmt.Sprintf(" · %d prompts as /mcp__%s__<name>", n, msg.name)
			}
			m.transcript.info(noteNotice + line + " (see /mcp)")
		}
		if m.status == "starting MCP servers…" {
			m.status = ""
		}
		m.refresh(false)
		return m, nil

	case shellDoneMsg:
		m.finishShell(msg)
		var cmd tea.Cmd
		if len(m.queued) > 0 {
			next := m.queued[0]
			m.queued = m.queued[1:]
			cmd = m.submit(next)
		}
		m.refresh(false)
		return m, cmd

	case agentDoneMsg:
		m.drainEvents()
		cmd := m.finishTurn(msg)
		if msg.err == nil && m.opts.Memory != nil && m.opts.AutoMemory && (m.opts.Agent.LastTurnMutated() || memory.LooksLikeFeedback(m.lastUserText)) {
			cmd = tea.Batch(cmd, m.extractMemory())
		}
		if msg.err == nil && m.opts.AutoTitle && m.opts.Session != nil && m.opts.Session.Meta.Title == "" && len(m.opts.Agent.Messages) >= 2 {
			cmd = tea.Batch(cmd, m.autoTitle())
		}
		m.refresh(false)
		return m, cmd
	case titleMsg:
		if msg.gen == m.titleGen && m.opts.Session != nil && m.opts.Session.Meta.Title == "" && msg.title != "" {
			if err := m.opts.Session.SetTitle(msg.title); err != nil {
				m.transcript.errorf("could not save the title: %v", err)
			}
			m.refresh(false)
			return m, tea.SetWindowTitle(m.windowTitle())
		}
		return m, nil

	case memoryMsg:
		if msg.err != nil {
			m.transcript.info(noteNotice + "memory: extraction failed: " + msg.err.Error())
		}
		for _, e := range msg.added {
			m.transcript.info(noteMemory + "remembered: " + memoryLine(e))
		}
		m.refresh(false)
		return m, nil
	}

	var cmd tea.Cmd
	// The footer grows and shrinks with the input box and with the
	// completion menu; the viewport has to be told, or the view is taller
	// than the terminal and the transcript scrolls under it.
	before := m.footerHeight()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	if m.footerHeight() != before {
		m.layout()
		m.refresh(false)
	}
	cmds = append(cmds, m.scroll(msg))
	return m, tea.Batch(cmds...)
}

func (m *model) drainEvents() {
	for _, ev := range m.queue.drain() {
		m.applyEvent(ev)
	}
}

func (m *model) applyEvent(ev agent.Event) {
	m.transcript.apply(ev)
	switch ev.Kind {
	case agent.EvThinking:
		m.phase = "thinking"
	case agent.EvText:
		m.phase = "writing"
	case agent.EvToolStart:
		m.phase = "preparing " + ev.Call.Name
	case agent.EvToolInput:
		// Streamed input: the status says what is being written and how
		// much, instead of "preparing write_file" for a minute.
		if e := m.transcript.lastTool(ev.Call); e != nil && e.call.Name != "" {
			if s := streamingSummary(e.call.Name, e.input.String(), m.opts.Cwd); s != "" && s != "…" {
				m.phase = e.call.Name + " " + s
			}
		}
	case agent.EvPhase:
		// The provider or the loop said what it is waiting on: a retry
		// countdown, a compaction, a subagent.
		m.phase = ev.Text
	case agent.EvToolRun:
		m.phase = "running " + ev.Call.Name
		if ev.Call.Name == "task" {
			m.phase = "waiting on subagent"
		}
	case agent.EvToolResult:
		m.phase = "thinking"
		if ev.Call != nil && ev.Call.Name == "goal_status" && !ev.IsError {
			m.status = strings.TrimSpace(ev.Output)
		}
	case agent.EvCompacted:
		m.phase = "thinking"
	case agent.EvVerdict:
		m.lastVerdict = ev.Text
	case agent.EvTurnDone:
		u := ev.Usage
		m.lastUsage = u
		in := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
		s := fmt.Sprintf("in %s · out %s", shortTokens(in), shortTokens(u.OutputTokens))
		if usd, ok := u.Cost(m.opts.Agent.Price); ok {
			s += " · " + llm.FormatUSD(usd)
			if u.CostSource(m.opts.Agent.Price) != "reported" {
				s += " est."
			}
		}
		if ev.StopReason != "" && ev.StopReason != "end_turn" {
			s += " · stopped: " + string(ev.StopReason)
		}
		m.lastTurn = s
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.picker != nil {
		return m.handlePickerKey(msg)
	}
	if m.connect != nil {
		return m.handleConnectKey(msg)
	}
	if m.pending == nil && msg.Paste && bigPaste(string(msg.Runes)) {
		// A file dropped into the prompt is one thing, not two hundred
		// lines to scroll past. It becomes a chip; submit puts it back.
		m.input.InsertString(m.storePaste(string(msg.Runes)))
		m.layout()
		m.status = fmt.Sprintf("pasted %s as a chip · it is sent in full", plural(strings.Count(string(msg.Runes), "\n")+1, "line"))
		m.refresh(false)
		return nil, true
	}
	// Permission prompt takes over the keyboard.
	if m.pending != nil && m.denying {
		// Collecting an optional reason: enter sends it, esc denies silently.
		switch msg.String() {
		case "enter":
			m.answer(agent.Deny, strings.TrimSpace(m.input.Value()))
			m.input.Reset()
			m.layout()
		case "esc":
			m.answer(agent.Deny, "")
			m.input.Reset()
			m.layout()
		case "ctrl+c":
			m.answer(agent.Deny, "")
			m.input.Reset()
			m.cancelTurn()
		default:
			return nil, false // let the textarea take it
		}
		return nil, true
	}
	if m.pending != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			m.answer(agent.Allow, "")
		case "a", "A":
			m.answer(agent.AllowSession, "")
		case "r", "R":
			m.answer(agent.AllowAlways, "")
		case "n", "N":
			// Ask why so the model can adapt; enter with nothing skips.
			m.denying = true
			m.input.Reset()
			m.phase = "waiting for your reason"
			m.layout()
			m.refresh(false)
		case "esc":
			m.answer(agent.Deny, "")
		case "c", "C", "ctrl+c":
			// Not just this call: the whole turn stops here.
			m.answer(agent.Deny, "the user cancelled the turn")
			m.cancelTurn()
		case "ctrl+o":
			// At a prompt ctrl+o is about this one call, not the session.
			if e := m.transcript.findTool(m.pending.call.ID); e != nil {
				e.expanded, e.final = !e.expanded, false
			}
			m.refresh(false)
		case "pgup", "pgdown":
			return m.scroll(msg), true
		}
		return nil, true
	}
	switch msg.String() {
	case "ctrl+c":
		// Three stages: cancel a running turn, else clear typed text, else
		// quit. A second press while a cancel is pending forces the quit.
		if m.running && !m.cancelling {
			m.cancelTurn()
			return nil, true
		}
		if !m.running && m.input.Value() != "" {
			m.input.Reset()
			m.status = "input cleared · ctrl+c again to quit"
			m.quitArmedAt = time.Now()
			m.layout()
			m.refresh(false)
			return nil, true
		}
		// One press when idle arms the quit; a second within a couple of
		// seconds does it. An accidental ctrl+c must not close a session.
		if !m.running && time.Since(m.quitArmedAt) > quitArmWindow {
			m.quitArmedAt = time.Now()
			m.status = "ctrl+c again to quit"
			m.refresh(false)
			return nil, true
		}
		m.quitting = true
		return tea.Quit, true
	case "ctrl+d":
		if m.input.Value() == "" {
			m.quitting = true
			return tea.Quit, true
		}
	case "esc":
		if m.running {
			m.cancelTurn()
			m.restoreQueued()
			return nil, true
		}
		if m.histIdx < len(m.history) {
			// Browsing the history: esc puts back what was being typed
			// before ↑ went looking, rather than throwing it away.
			m.input.SetValue(m.draft)
			m.input.CursorEnd()
			m.histIdx = len(m.history)
			m.layout()
			m.refresh(false)
			return nil, true
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.layout()
			m.refresh(false)
			return nil, true
		}
	case "ctrl+o":
		switch expanded, any := m.transcript.toggleLastTurn(); {
		case !any:
			m.status = "nothing folded in the last turn · /verbose expands every turn"
		case expanded:
			m.status = "expanded the last turn · ctrl+o folds it again, click a line to fold one"
		default:
			m.status = "folded the last turn"
		}
		m.refresh(false)
		return nil, true
	case "backspace":
		// The chip is one thing: backspace takes all of it or none.
		if v := m.input.Value(); pasteMarkerRe.MatchString(v) {
			if loc := pasteMarkerRe.FindAllStringIndex(v, -1); loc[len(loc)-1][1] == len(v) {
				m.input.SetValue(v[:loc[len(loc)-1][0]])
				m.input.CursorEnd()
				m.layout()
				m.refresh(false)
				return nil, true
			}
		}
	case "ctrl+g":
		// A build or a test run that turns out to be long used to leave
		// only waiting or cancelling. Promotion keeps the work: the
		// command becomes a job, and the turn carries on.
		job, err := m.opts.Running.PromoteLatest()
		if err != nil {
			m.status = err.Error()
			m.refresh(false)
			return nil, true
		}
		m.transcript.info(fmt.Sprintf("moved to background as job %d · /jobs shows it", job))
		m.status = fmt.Sprintf("moved to background as job %d", job)
		m.refresh(false)
		return nil, true
	case "ctrl+s":
		// Say something to the turn that is running, rather than queueing
		// it for the next one. The agent holds the note and hands it to
		// the model before its next call.
		text := strings.TrimSpace(m.input.Value())
		switch {
		case !m.running:
			m.status = "ctrl+s steers a running turn; there is none · enter sends"
		case text == "":
			m.status = "type what the model should do differently, then ctrl+s"
		default:
			m.opts.Typed.Note(text)
			m.opts.Agent.Steer(m.inflatePastes(text))
			m.steering = append(m.steering, text)
			m.remember(text)
			m.input.Reset()
			m.layout()
		}
		m.refresh(false)
		return nil, true
	case "ctrl+l":
		return tea.ClearScreen, true
	case "ctrl+v":
		// Text paste arrives through the terminal on its own; ctrl+v grabs an
		// image from the clipboard and attaches it as an @mention.
		m.pasteImage()
		return nil, true
	case "tab":
		if m.complete() {
			m.layout()
			m.refresh(false)
			return nil, true
		}
	case "up":
		if m.moveMenu(-1) {
			return nil, true
		}
		if m.running && len(m.queued) > 0 && m.input.Value() == "" {
			// Take the last queued message back to edit it: queued text
			// was unreachable until the turn ended.
			m.input.SetValue(m.queued[len(m.queued)-1])
			m.queued = m.queued[:len(m.queued)-1]
			m.input.CursorEnd()
			m.layout()
			m.status = "took the queued message back · enter queues it again, ctrl+s steers this turn"
			m.refresh(false)
			return nil, true
		}
		if m.input.Line() == 0 && len(m.history) > 0 && m.histIdx > 0 {
			if m.histIdx == len(m.history) {
				m.draft = m.input.Value()
			}
			m.histIdx--
			m.input.SetValue(m.recall(m.histIdx))
			m.input.CursorEnd()
			m.layout()
			return nil, true
		}
	case "down":
		if m.moveMenu(1) {
			return nil, true
		}
		if m.histIdx < len(m.history) && m.input.Line() == m.input.LineCount()-1 {
			m.histIdx++
			if m.histIdx == len(m.history) {
				m.input.SetValue(m.draft)
			} else {
				m.input.SetValue(m.recall(m.histIdx))
			}
			m.input.CursorEnd()
			m.layout()
			return nil, true
		}
	case "pgup", "pgdown", "ctrl+u", "ctrl+b", "ctrl+f":
		// With text in the input these are the textarea's readline keys
		// (kill line, back/forward a character); they page the transcript
		// only when there is nothing to edit.
		if k := msg.String(); k == "pgup" || k == "pgdown" || m.input.Value() == "" {
			return m.scroll(msg), true
		}
		return nil, false
	case "enter":
		if c, idx := m.menu(); len(c) > 0 && !m.compArgs && strings.TrimSpace(m.input.Value()) != c[idx].name {
			m.complete()
			m.layout()
			m.refresh(false)
			if c[idx].args != "" || !strings.HasPrefix(c[idx].name, "/") {
				return nil, true // needs arguments or a path: let the user continue
			}
			m.input.SetValue(c[idx].name)
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil, true
		}
		// A "y" typed a moment after a permission prompt closed was meant
		// for the prompt, not for the model; sending it costs a turn of
		// the model guessing what "y" means.
		if len(text) == 1 && strings.ContainsAny(strings.ToLower(text), "ynar") && time.Since(m.pendingClosedAt) < strayKeyWindow {
			m.input.Reset()
			m.layout()
			m.status = "ignored a stray " + text + " meant for the approval prompt"
			m.refresh(false)
			return nil, true
		}
		if m.running && !strings.HasPrefix(text, "/") {
			// Queue it; finishTurn sends it once the model is done.
			m.queued = append(m.queued, text)
			m.remember(text)
			m.input.Reset()
			m.layout()
			m.status = fmt.Sprintf("queued %d · sends when this turn ends · esc cancels the turn and restores it", len(m.queued))
			m.refresh(false)
			return nil, true
		}
		m.input.Reset()
		m.layout()
		m.remember(text)
		if strings.HasPrefix(text, "/") {
			return m.command(text), true
		}
		if strings.HasPrefix(text, "!") {
			return m.shell(strings.TrimSpace(text[1:])), true
		}
		return m.submit(text), true
	}
	return nil, false
}

// pasteImage saves the clipboard image to a file and inserts @path.
func (m *model) pasteImage() {
	data, err := m.clipboard()
	if err != nil {
		m.status = err.Error()
		m.refresh(false)
		return
	}
	path, err := saveClipboardImage(data)
	if err != nil {
		m.status = "could not save the image: " + err.Error()
		m.refresh(false)
		return
	}
	v := m.input.Value()
	if v != "" && !strings.HasSuffix(v, " ") && !strings.HasSuffix(v, "\n") {
		m.input.InsertString(" ")
	}
	m.input.InsertString("@" + path + " ")
	m.layout()
	m.status = fmt.Sprintf("attached clipboard image (%s) · send when ready", shortBytes(int64(len(data))))
	m.refresh(false)
}

// remember adds a submitted prompt to the up/down history and to the
// file that outlives the process. A "!" command keeps its flag so
// recalling it restores the shell prompt rather than asking the model.
func (m *model) remember(text string) {
	e := histEntry{Text: text}
	if strings.HasPrefix(text, "!") {
		e = histEntry{Text: strings.TrimPrefix(text, "!"), Bash: true}
	}
	if e.Text == "" {
		return
	}
	if n := len(m.history); n == 0 || m.history[n-1] != e {
		m.history = append(m.history, e)
		if m.histFile != "" {
			if err := appendPromptHistory(m.histFile, e, len(m.history)); err == nil && len(m.history) > histCap {
				m.history = m.history[len(m.history)-histCap:]
				rewritePromptHistory(m.histFile, m.history)
			}
		}
	}
	m.histIdx = len(m.history)
	m.draft = ""
}

// recall renders one history entry back into the input's own language.
func (m *model) recall(i int) string {
	e := m.history[i]
	if e.Bash {
		return "!" + e.Text
	}
	return e.Text
}

// completions lists slash commands matching the input, or files matching
// the @path being typed at its end.
func (m *model) completions() []command {
	if m.connect != nil || m.picker != nil {
		return nil
	}
	m.compArgs = false
	v := m.input.Value()
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " \n") {
		var exact, fuzzy []command
		for _, c := range m.allCommands() {
			switch {
			case strings.HasPrefix(c.name, v):
				exact = append(exact, c)
			case fuzzyCommand(c.name, v):
				fuzzy = append(fuzzy, c)
			}
		}
		return append(exact, fuzzy...)
	}
	if name, arg, ok := strings.Cut(v, " "); ok && strings.HasPrefix(name, "/") && !strings.Contains(arg, "\n") {
		if c, found := lookup(name); found && c.argsFor != nil {
			// An argument menu offers; it never takes enter, so a typed
			// argument still runs the command the moment you send it.
			m.compArgs = true
			return c.argsFor(m, strings.TrimLeft(arg, " "))
		}
	}
	if word, ok := mentionAtEnd(v); ok {
		var out []command
		for _, p := range prompt.CompletePath(m.opts.Cwd, word) {
			help := "file"
			if strings.HasSuffix(p, "/") {
				help = "directory"
			}
			out = append(out, command{name: "@" + p, help: help})
		}
		return out
	}
	return nil
}

// allCommands is the built-in table followed by the user's custom commands
// and the prompts MCP servers offer.
func (m *model) allCommands() []command {
	out := append([]command(nil), commands...)
	names := make([]string, 0, len(m.custom))
	for n := range m.custom {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := m.custom[n]
		out = append(out, command{name: n, args: "[args]", help: sanitize(c.Help) + " · " + shortPath(c.Path)})
	}
	for _, sk := range skill.Load(m.opts.Cwd, m.opts.ConfigDir) {
		if sk.UserInvocable && m.custom["/"+sk.Name].Name == "" {
			out = append(out, command{name: "/" + sanitize(sk.Name), args: "[args]", help: sanitize(sk.Description) + " · skill"})
		}
	}
	if m.opts.MCP != nil && m.opts.MCP.Manager != nil {
		for _, st := range m.opts.MCP.Manager.Status() {
			for _, p := range st.Prompts {
				args := make([]string, 0, len(p.Arguments))
				for _, a := range p.Arguments {
					if a.Required {
						args = append(args, "<"+a.Name+">")
					} else {
						args = append(args, "["+a.Name+"]")
					}
				}
				help := sanitize(strings.TrimSpace(p.Description))
				if help == "" {
					help = "prompt from " + st.Name
				}
				out = append(out, command{name: "/" + mcp.ToolName(st.Name, p.Name), args: strings.Join(args, " "), help: help + " · MCP " + st.Name})
			}
		}
	}
	return out
}

// mcpPrompts lists the prompts one server offers.
func (m *model) mcpPrompts(server string) []mcp.PromptInfo {
	if m.opts.MCP == nil || m.opts.MCP.Manager == nil {
		return nil
	}
	c, ok := m.opts.MCP.Manager.Client(server)
	if !ok {
		return nil
	}
	return c.Prompts()
}

// runMCPPrompt resolves "/mcp__server__prompt arg1 arg2" against the servers:
// positional words fill the prompt's declared arguments in order, the last
// one taking the rest of the line. The rendered text is submitted as the
// user's turn.
func (m *model) runMCPPrompt(name string, rest string) (tea.Cmd, bool) {
	if m.opts.MCP == nil || m.opts.MCP.Manager == nil || !strings.HasPrefix(name, "/mcp__") {
		return nil, false
	}
	for _, st := range m.opts.MCP.Manager.Status() {
		for _, p := range st.Prompts {
			if "/"+mcp.ToolName(st.Name, p.Name) != name {
				continue
			}
			c, ok := m.opts.MCP.Manager.Client(st.Name)
			if !ok {
				return nil, false
			}
			args := map[string]string{}
			words := strings.Fields(rest)
			for i, a := range p.Arguments {
				switch {
				case i >= len(words):
					if a.Required {
						m.transcript.errorf("%s needs <%s>", name, a.Name)
						m.refresh(false)
						return nil, true
					}
				case i == len(p.Arguments)-1:
					args[a.Name] = strings.Join(words[i:], " ")
				default:
					args[a.Name] = words[i]
				}
			}
			m.startTurn("rendering prompt from " + st.Name)
			send, promptName := m.send, p.Name
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				text, err := c.GetPrompt(ctx, promptName, args)
				send(mcpPromptMsg{command: name, text: text, err: err})
			}()
			return m.spinner.Tick, true
		}
	}
	return nil, false
}

// mentionAtEnd returns the partial path of an @mention that ends the input.
func mentionAtEnd(v string) (string, bool) {
	i := strings.LastIndexAny(v, " \n\t")
	word := v[i+1:]
	if !strings.HasPrefix(word, "@") {
		return "", false
	}
	return word[1:], true
}

const menuRows = 8

// menu returns the completion rows and the highlighted index, resetting
// the highlight whenever the input changed since it was set.
func (m *model) menu() ([]command, int) {
	c := m.completions()
	if len(c) == 0 {
		return nil, 0
	}
	if v := m.input.Value(); v != m.compFor {
		m.compFor, m.compIdx = v, 0
	}
	if m.compIdx >= len(c) {
		m.compIdx = len(c) - 1
	}
	return c, m.compIdx
}

// moveMenu shifts the highlight; it reports whether a menu was open.
func (m *model) moveMenu(delta int) bool {
	c, idx := m.menu()
	if len(c) == 0 {
		return false
	}
	m.compIdx = (idx + delta + len(c)) % len(c)
	m.compFor = m.input.Value()
	m.layout()
	m.refresh(false)
	return true
}

// complete applies the highlighted completion to the input. For paths with
// several matches and no explicit choice it extends to the common prefix so
// repeated tabs narrow the list.
func (m *model) complete() bool {
	c, idx := m.menu()
	if len(c) == 0 {
		return false
	}
	// One rule for both menus: with the highlight where it started and
	// several matches, extend to what they all share so a second tab
	// narrows the list; otherwise take the highlighted row.
	v := m.input.Value()
	i := strings.LastIndexAny(v, " \n\t")
	name := c[idx].name
	if idx == 0 && len(c) > 1 {
		for _, o := range c[1:] {
			name = commonPrefix(name, o.name)
		}
		if name == v[i+1:] { // nothing to extend: take the highlighted one
			name = c[idx].name
		}
	}
	if !strings.HasSuffix(name, "/") && (len(c) == 1 || name == c[idx].name) {
		name += " "
	}
	m.input.SetValue(v[:i+1] + name)
	m.input.CursorEnd()
	m.compFor, m.compIdx = m.input.Value(), 0
	return true
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func shortBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func (m *model) answer(d agent.Decision, reason string) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- verdict{d: d, reason: reason}
	m.transcript.permissionAnswered(m.pending.call, d)
	m.pending = nil
	m.pendingClosedAt = time.Now()
	m.denying = false
	m.phase = "thinking"
	m.refresh(false)
}

func (m *model) submit(text string) tea.Cmd {
	text = m.inflatePastes(text)
	if m.opts.Agent.Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect")
		m.refresh(false)
		return nil
	}
	m.transcript.user(text)
	// A host the user just named is a host the user is asking about, so
	// fetch_url may reach it even on a private network. Only main seeded
	// this, from the first prompt, which left "look at
	// http://localhost:8080/health" refused in the message that asked for
	// it.
	m.opts.Typed.Note(text)
	m.lastUserText = text
	text, atts := prompt.ExpandMentions(m.opts.Cwd, text)
	if len(atts) > 0 {
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
			switch {
			case a.Image():
				names[i] += " (image, " + shortBytes(a.Size) + ")"
			case !a.Dir:
				names[i] += " (" + shortBytes(a.Size) + ")"
			}
		}
		m.transcript.info(noteAttached + "attached " + strings.Join(names, " · "))
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: text}}, prompt.ImageBlocks(atts)...)
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.repin()
	m.refresh(false)
	go func() {
		// Every message reached the session file as it happened; there is
		// nothing left to write when the turn ends.
		err := ag.RunBlocks(ctx, blocks, queue.push)
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

// shell runs a command the user typed with a "!" prefix in the project
// directory, shows it like a tool call, and hands the output to the model
// as context for the next turn.
func (m *model) shell(cmdline string) tea.Cmd {
	cmdline = m.inflatePastes(cmdline)
	if cmdline == "" {
		m.transcript.errorf("usage: !<command> · runs in %s and shares the output with the model", shortPath(m.opts.Cwd))
		m.refresh(false)
		return nil
	}
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before running a command")
		m.refresh(false)
		return nil
	}
	// A command the user ran themselves names hosts as much as a prompt
	// does: curl localhost:3000 is the user pointing at it.
	m.opts.Typed.Note(cmdline)
	input, _ := json.Marshal(map[string]string{"command": cmdline})
	call := agent.ToolCall{ID: fmt.Sprintf("shell-%d", time.Now().UnixNano()), Name: "shell", Input: input}
	m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	m.startTurn("running your command")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	send, cwd := m.send, m.opts.Cwd
	m.repin()
	m.refresh(false)
	go func() {
		start := time.Now()
		out, err := tool.Bash{Root: cwd}.Run(ctx, input)
		send(shellDoneMsg{call: call, output: out, err: err, dur: time.Since(start)})
	}()
	return m.spinner.Tick
}

func (m *model) finishShell(msg shellDoneMsg) {
	m.running = false
	m.cancelling = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.phase = ""
	out, isErr := msg.output, false
	if msg.err != nil {
		out, isErr = "Error: "+msg.err.Error(), true
	}
	if out == "" {
		out = "(no output)"
	}
	m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: &msg.call, Output: out, IsError: isErr, Duration: msg.dur})
	var in struct{ Command string }
	json.Unmarshal(msg.call.Input, &in)
	m.opts.Agent.AddContext(fmt.Sprintf("I ran this command myself in %s:\n$ %s\n<output>\n%s\n</output>", m.opts.Cwd, in.Command, strings.TrimRight(out, "\n")))
	m.status = "ran in " + shortDuration(msg.dur) + " · output shared with the model"
	if len(m.queued) > 0 {
		m.status += " · sending queued prompt"
	}
}

func (m *model) startTurn(phase string) {
	m.steering = nil
	m.term.turnStarted()
	m.running = true
	m.cancelling = false
	m.startedAt = time.Now()
	m.status = ""
	m.lastVerdict = ""
	m.phase = phase
}

// onOff names a toggle's new state, so every "/x is on" line reads the
// same way.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// plural is "1 file" or "3 files".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// compact summarises the history in the background like a turn.
func (m *model) compact() tea.Cmd {
	m.startTurn("compacting context")
	m.compacting = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.transcript.info(fmt.Sprintf("compacting ~%s tokens of context…", shortTokens(ag.EstimateTokens())))
	m.refresh(false)
	go func() {
		// Compaction appends the summary and the seqs it replaces to the
		// same log; the session file stays as it is.
		err := ag.ForceCompact(ctx, queue.push)
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) cancelTurn() {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancelling = true
	m.phase = "cancelling"
	m.status = "cancelling… (ctrl+c again to force quit)"
}

func (m *model) finishTurn(msg agentDoneMsg) tea.Cmd {
	m.running = false
	m.cancelling = false
	compacting := m.compacting
	m.compacting = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.pending = nil
	m.pendingClosedAt = time.Now()
	m.phase = ""
	elapsed := shortDuration(time.Since(m.startedAt))
	m.transcript.finishTurn()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("cancelled")
		m.transcript.errorf("cancelled after %s", elapsed)
		m.status = "cancelled"
	case errors.Is(msg.err, agent.ErrMaxIterations):
		m.transcript.errorf("stopped after %d tool rounds in one turn · send a message to continue", m.opts.Agent.MaxIterations)
		m.status = "stopped"
	case errors.Is(msg.err, agent.ErrStuck):
		m.transcript.errorf("stopped: the model kept repeating the same tool call · its changes so far are kept; tell it what to do differently")
		m.status = "stuck"
	case msg.err != nil:
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("the turn ended before this call returned")
		m.transcript.errorf("%s", friendlyError(msg.err, m.opts.Provider))
		m.status = "error"
	case compacting:
		m.status = "compacted in " + elapsed
	default:
		m.status = "done in " + elapsed
		if b := verdictBrief(m.lastVerdict); b != "" {
			m.status = b + " · " + m.status
		}
		if m.lastTurn != "" {
			m.status += " · " + m.lastTurn
		}
		if n := m.filesChangedLastTurn(); n > 0 {
			m.status += fmt.Sprintf(" · %s changed · /undo reverts", plural(n, "file"))
			if m.opts.Verify != nil && len(m.opts.Verify.Config.Commands) == 0 && !m.verifyTipShown {
				m.verifyTipShown = true
				m.transcript.tip("/verify set \"go test ./...\" runs the project's checks after every change and sends failures back to the model; it is saved in .janetcode/verify.json")
			}
		}
	}
	m.reportCacheWaste()
	m.steering = nil
	m.term.turnEnded(msg.err == nil, m.status)
	if msg.err == nil && len(m.queued) == 0 {
		if cont := m.opts.Agent.GoalContinuation(); cont != "" {
			m.transcript.info(noteGoal + "goal continuing")
			return m.submit(cont)
		}
	}
	if len(m.queued) == 0 {
		return nil
	}
	if msg.err != nil {
		// Do not fire queued prompts into a failed or cancelled turn; hand
		// them back so nothing typed is lost.
		m.restoreQueued()
		return nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	return m.submit(next)
}

// friendlyError turns a provider failure into one shape:
//
//	Headline (code): why. What to do.
//
// The headline says what failed in three words, the code is what you
// would search for, the reason is the provider's own words, and the last
// sentence is the one thing to try. A bare Go error is none of those.
func friendlyError(err error, provider string) string {
	s := strings.TrimSpace(err.Error())
	l := strings.ToLower(s)
	shape := func(head, code, todo string) string {
		if code != "" {
			head += " (" + code + ")"
		}
		why := strings.TrimRight(s, ".")
		return head + ": " + why + ". " + todo
	}
	switch {
	case strings.Contains(l, "connection refused") || strings.Contains(l, "no such host") || strings.Contains(l, "dial tcp"):
		return shape("cannot reach the API", errorCode(l), "Check the base URL for provider "+provider+" with /connect, and your network.")
	case strings.Contains(l, "http 401") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		return shape("authentication failed", errorCode(l), "Check the API key for provider "+provider+" with /connect.")
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		return shape("rate limited or out of quota", errorCode(l), "Wait and resend, add keys with /connect, or switch model with /model.")
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"),
		strings.Contains(l, "http 400") && strings.Contains(l, "model"):
		return shape("model not available", errorCode(l), "Pick another with /model.")
	case strings.Contains(l, "stream stalled"):
		return shape("the model stopped sending", errorCode(l), "Resend it; raise stream_idle_timeout or stream_max_duration in the config if it keeps happening on long answers.")
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return shape("the request timed out", errorCode(l), "Resend it; if it keeps happening the model may be overloaded.")
	case strings.Contains(l, "http 5"):
		return shape("the provider failed", errorCode(l), "Resend it; the failure is on their side.")
	}
	return s
}

// errorCode is the short thing you would search for: the HTTP status when
// there is one, otherwise the kind of network failure.
var httpStatusRe = regexp.MustCompile(`(?:http |status |code )(\d{3})`)

func errorCode(l string) string {
	if m := httpStatusRe.FindStringSubmatch(l); m != nil {
		return "http " + m[1]
	}
	switch {
	case strings.Contains(l, "no such host"):
		return "dns"
	case strings.Contains(l, "connection refused"):
		return "refused"
	case strings.Contains(l, "stream stalled"):
		return "stalled stream"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "timeout"
	}
	return ""
}

func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
	if m.running {
		if c, ok := lookup(fields[0]); ok && c.avail == availIdle {
			m.transcript.errorf("%s waits for the turn to finish (esc cancels it, ctrl+s steers it)", fields[0])
			m.refresh(false)
			return nil
		}
	}
	switch fields[0] {
	case "/help", "/?":
		m.transcript.info(m.helpText())
	case "/quit", "/exit", "/q":
		m.quitting = true
		return tea.Quit
	case "/clear", "/new":
		sess, err := session.New(dirOf(m.opts.Session), m.opts.Cwd, m.opts.Provider, m.opts.Agent.Model)
		if err != nil {
			m.transcript.errorf("new session: %v", err)
			break
		}
		m.opts.Session = sess
		m.opts.Agent.Messages = nil
		m.opts.Agent.SetLog(sess)
		m.opts.Agent.UndoPath = sess.UndoPath()
		m.titleGen++ // a title generated for the old conversation must not land here
		m.transcript = newTranscriptStyled(m.transcript.styleName)
		m.transcript.banner(m.welcome(""))
		m.transcript.info("new session " + sess.Meta.ID)
		m.lastTurn, m.lastVerdict, m.status = "", "", ""
		m.pinned = true
	case "/yolo":
		m.opts.Agent.AutoApprove = !m.opts.Agent.AutoApprove
		if m.opts.Agent.AutoApprove {
			m.transcript.info("auto-approve on · tools run without asking")
		} else {
			m.transcript.info("auto-approve off · edits and commands ask first")
		}
	case "/edits":
		m.opts.Agent.AutoEdits = !m.opts.Agent.AutoEdits
		if m.opts.Agent.AutoEdits {
			m.transcript.info("auto-accept edits on · file changes go through, commands still ask")
		} else {
			m.transcript.info("auto-accept edits off · edits ask first")
		}
	case "/settle":
		if m.opts.Session == nil {
			m.transcript.errorf("no session to settle")
			break
		}
		settled := !m.opts.Session.Meta.Settled
		if err := m.opts.Session.SetSettled(settled); err != nil {
			m.transcript.errorf("settle: %v", err)
		} else if settled {
			m.transcript.info("session settled: it lists after the active ones in /resume until something happens in it")
		} else {
			m.transcript.info("session active again")
		}
	case "/readonly":
		m.opts.Agent.ReadOnly = !m.opts.Agent.ReadOnly
		if m.opts.Agent.ReadOnly {
			m.transcript.info("read-only on · the model can inspect but not change anything")
		} else {
			m.transcript.info("read-only off")
		}
	case "/plan":
		return m.planCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/trajectory":
		m.trajectoryCommand(fields[1:])
	case "/feedback":
		m.feedbackCommand(fields[1:])
	case "/goal":
		return m.goalCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/agents":
		m.agentsCommand()
	case "/verbose":
		m.verbose = !m.verbose
		m.transcript.info("verbose tool output " + onOff(m.verbose))
	case "/connect":
		return m.openConnect(stepPick)
	case "/model":
		if len(fields) < 2 {
			if m.opts.Config == nil {
				m.transcript.info("model: " + m.opts.Agent.Model + " (" + m.opts.Provider + ")")
				break
			}
			return m.openConnect(stepModel)
		}
		if cfg := m.opts.Config; cfg != nil && m.opts.Models != nil {
			// The name may belong to a provider other than the one in
			// use; follow it rather than sending it to the wrong endpoint.
			if name, ok := cfg.RouteFor(fields[1]); ok && name != cfg.Provider {
				prov, _, err := m.opts.Models(fields[1])
				if err != nil {
					m.transcript.errorf("%v", err)
					break
				}
				m.opts.Agent.Provider = prov
				cfg.Provider = name
				m.opts.Provider = name
				m.transcript.info("switched to provider " + name + " for " + fields[1])
			}
		}
		m.opts.Agent.Model = fields[1]
		m.applyModelMeta(fields[1])
		if cfg := m.opts.Config; cfg != nil {
			cfg.Model = fields[1]
			if m.opts.ConfigPath != "" {
				if err := config.Update(m.opts.ConfigPath, cfg.Provider, cfg.Active(), fields[1]); err != nil {
					m.transcript.errorf("saving config: %v", err)
				}
			}
		}
		m.transcript.info("model set to " + fields[1] + " on " + m.opts.Provider)
	case "/mcp":
		m.mcpCommand(fields[1:])
	case "/hooks":
		m.hooksCommand(fields[1:])
	case "/permissions", "/perms":
		m.permissionsCommand(fields[1:])
	case "/lsp":
		m.lspCommand()
	case "/jobs":
		m.jobsCommand(fields[1:])
	case "/open":
		m.openCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/diff":
		m.diffCommand(fields[1:])
	case "/fork":
		m.forkSession(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/title":
		m.setTitle(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/history":
		m.searchSessions(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/reload":
		m.opts.Agent.RefreshExtra()
		m.transcript.info("memory and skill indexes will be rebuilt for the next call (one cache miss); they also refresh at compaction")
	case "/context", "/ctx":
		m.contextCommand()
	case "/todos", "/todo":
		m.transcript.info(agent.RenderTodos(m.opts.Agent.Todos()))
	case "/inspect", "/i":
		m.inspectCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/pin":
		m.pinCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/unpin":
		m.unpinCommand(fields[1:])
	case "/verify":
		if cmd := m.verifyCommand(fields[1:]); cmd != nil {
			return cmd
		}
	case "/skills":
		skills := skill.Load(m.opts.Cwd, m.opts.ConfigDir)
		if len(skills) == 0 {
			m.transcript.info("no skills found: add .janetcode/skills/<name>/SKILL.md (or <name>.md) to the project, or skills/ under " + shortPath(m.opts.ConfigDir) + "; .claude/skills is read too")
			break
		}
		var sb strings.Builder
		for _, s := range skills {
			fmt.Fprintf(&sb, "%-20s %s  (%s)\n", s.Name, s.Description, s.Source)
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	case "/rules":
		rs := rules.Load(m.opts.Cwd, m.opts.ConfigDir)
		if len(rs) == 0 {
			m.transcript.info("no path-scoped rules: add .janetcode/rules/<name>.md with a paths: list in its frontmatter, and it is sent once, when a tool call touches a matching path")
			break
		}
		var sb strings.Builder
		for _, r := range rs {
			state := "waiting"
			if m.opts.Rules.Sent(r.Name) {
				state = "sent"
			}
			fmt.Fprintf(&sb, "%-18s %-7s %-26s %s\n", r.Name, state, strings.Join(r.Paths, ","), r.Description)
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	case "/memory", "/remember", "/forget":
		m.memoryCommand(fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/undo", "/redo":
		if n, err := strconv.Atoi(strings.Join(fields[1:], "")); err == nil && fields[0] == "/undo" {
			m.undoChange(n)
		} else {
			m.undoCommand(fields[0], len(fields) > 1 && fields[1] == "preview")
		}
	case "/changes":
		m.changesCommand(fields[1:])
	case "/blame":
		m.blameCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/find", "/search":
		q := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		if q == "" {
			q = m.lastFind
		}
		if q == "" {
			m.transcript.errorf("usage: /find <text>")
			break
		}
		m.find(q)
		return nil
	case "/resume", "/sessions":
		if len(fields) < 2 {
			return m.openSessionPicker()
		}
		m.resume(fields[1])
	case "/compact":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before compacting")
			break
		}
		return m.compact()
	case "/export":
		path := ""
		if len(fields) > 1 {
			path = strings.Join(fields[1:], " ")
		}
		m.export(path)
	case "/stats":
		m.statsCommand()
	case "/usage", "/cost":
		u := m.opts.Agent.UsageSnapshot()
		line := fmt.Sprintf("session tokens · in %s · out %s · cache read %s · cache write %s · context now ~%s",
			shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens), shortTokens(m.opts.Agent.EstimateTokens()))
		switch usd, ok := u.Cost(m.opts.Agent.Price); {
		case ok && u.CostUSD > 0:
			line += "\ncost " + llm.FormatUSD(usd) + " (reported by the provider)"
		case ok:
			line += "\ncost " + llm.FormatUSD(usd) + " at list price (an API-equivalent estimate from the config's \"prices\" or LiteLLM's rate table, not money spent)"
		default:
			line += "\ncost unpriced: the provider does not report it and no rate is known for this model; add it under \"prices\" in the config file, or wait for the rate table to be fetched"
		}
		m.transcript.info(line)
	default:
		if c, ok := m.custom[fields[0]]; ok {
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			return m.submit(c.Expand(args))
		}
		for _, sk := range skill.Load(m.opts.Cwd, m.opts.ConfigDir) {
			if sk.UserInvocable && "/"+sk.Name == fields[0] {
				m.transcript.info("running skill " + sk.Name + " (" + shortPath(sk.Path) + ")")
				return m.submit(sk.Expand(strings.TrimPrefix(text, fields[0])))
			}
		}
		if cmd, ok := m.runMCPPrompt(fields[0], strings.TrimPrefix(text, fields[0])); ok {
			return cmd
		}
		m.transcript.errorf("unknown command %s (try /help)", fields[0])
	}
	m.refresh(false)
	return nil
}

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// find scrolls the transcript to the next line containing q (case-insensitive),
// starting below the current top line and wrapping around.
// export writes the conversation as Markdown, to path or to a file named
// after the session in the working directory.
func (m *model) export(path string) {
	if path == "" {
		path = "janetcode-" + m.opts.Session.Meta.ID + ".md"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.Cwd, path)
	}
	md := session.Markdown(m.opts.Session.Meta, m.opts.Agent.Messages)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.transcript.errorf("export: %v", err)
		return
	}
	if err := tool.AtomicWrite(path, []byte(md)); err != nil {
		m.transcript.errorf("export: %v", err)
		return
	}
	m.transcript.info(fmt.Sprintf("exported %d messages to %s", len(m.opts.Agent.Messages), shortPath(path)))
}

func (m *model) find(q string) {
	// A repeat continues past the previous hit even when the viewport could
	// not scroll down to it (matches near the bottom).
	from := m.viewport.YOffset
	if q == m.lastFind && m.lastFindAt > from {
		from = m.lastFindAt
	} else if q != m.lastFind {
		from = -1
	}
	m.lastFind = q
	lines := strings.Split(m.lastContent, "\n")
	needle := strings.ToLower(q)
	var hits []int
	for i, l := range lines {
		if strings.Contains(strings.ToLower(ansiSeq.ReplaceAllString(l, "")), needle) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		m.status = fmt.Sprintf("no match for %q", q)
		return
	}
	next := hits[0]
	idx := 0
	for i, h := range hits {
		if h > from {
			next, idx = h, i
			break
		}
	}
	m.lastFindAt = next
	m.viewport.SetYOffset(next)
	m.pinned = m.viewport.AtBottom()
	m.status = fmt.Sprintf("match %d/%d for %q on line %d · /find again for the next", idx+1, len(hits), q, next+1)
}

func (m *model) mcpCommand(args []string) {
	o := m.opts.MCP
	if o == nil || o.Manager == nil {
		m.transcript.info("MCP is not configured: add servers under \"mcp_servers\" in the config file or a .mcp.json in the project")
		return
	}
	if len(args) > 0 && (args[0] == "auth" || args[0] == "logout") {
		m.mcpAuth(args)
		return
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case len(o.Project) == 0:
			m.transcript.errorf("no %s in %s", mcp.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.transcript.info("project servers are already trusted")
		default:
			if err := o.Trust.Trust(m.opts.Cwd, o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			m.transcript.info("trusted " + mcp.ProjectFile + " · starting its servers")
			for name, cfg := range o.Project {
				o.Manager.Start(context.Background(), name, cfg, func(name string, c *mcp.Client, err error) {
					n := 0
					if c != nil {
						n = len(c.Tools())
					}
					m.send(mcpDoneMsg{name: name, tools: n, err: err})
				})
			}
		}
		return
	}
	st := o.Manager.Status()
	if len(st) == 0 {
		if len(o.Project) > 0 && !o.ProjectTrusted {
			m.transcript.info("no MCP servers running · /mcp trust starts the ones in " + mcp.ProjectFile)
		} else {
			m.transcript.info("no MCP servers configured")
		}
		return
	}
	var sb strings.Builder
	sb.WriteString("MCP servers\n")
	for _, s := range st {
		switch {
		case s.Starting:
			fmt.Fprintf(&sb, "  … %s starting\n", s.Name)
		case s.Err != nil:
			fmt.Fprintf(&sb, "  ✗ %s: %v\n", s.Name, s.Err)
		default:
			fmt.Fprintf(&sb, "  ✓ %s · %d tools\n", s.Name, len(s.Tools))
			for _, t := range s.Tools {
				fmt.Fprintf(&sb, "      %s\n", t)
			}
			for _, p := range s.Prompts {
				fmt.Fprintf(&sb, "      /%s  (prompt)\n", mcp.ToolName(s.Name, p.Name))
			}
		}
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

// resume swaps the conversation for a saved one.
func (m *model) resume(arg string) {
	dir := dirOf(m.opts.Session)
	id := arg
	if n, err := strconv.Atoi(arg); err == nil {
		infos, err := session.List(dir, m.opts.Cwd)
		if err != nil {
			m.transcript.errorf("list sessions: %v", err)
			return
		}
		k := 0
		id = ""
		for _, in := range infos {
			if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
				continue
			}
			if k++; k == n {
				id = in.Meta.ID
				break
			}
		}
		if id == "" {
			m.transcript.errorf("no session numbered %d (see /resume)", n)
			return
		}
	}
	sess, history, err := session.Open(dir, id)
	if err != nil {
		m.transcript.errorf("resume %s: %v", id, err)
		return
	}
	m.opts.Session = sess
	m.opts.Agent.Messages = history
	m.opts.Agent.Pins = append([]string(nil), sess.Meta.Pins...)
	m.opts.Agent.SetLog(sess)
	m.opts.Agent.Usage = llm.Usage{}
	m.opts.Agent.UndoPath = sess.UndoPath()
	if err := m.opts.Agent.LoadUndo(); err != nil {
		m.transcript.errorf("%v", err)
	}
	m.titleGen++ // a title generated for the session we just left must not land here
	m.transcript = newTranscriptStyled(m.transcript.styleName)
	m.transcript.banner(m.welcome(resumedLine(sess.Meta.ID, len(history))))
	m.transcript.loadHistory(history)
	m.lastTurn = ""
	m.lastVerdict = ""
	m.repin()
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func dirOf(s *session.Session) string {
	return filepath.Dir(s.Path())
}

func (m *model) helpText() string {
	var sb strings.Builder
	sb.WriteString("keys\n")
	sb.WriteString("  enter send · ctrl+j / alt+enter newline · up/down recall prompts, or move in an open menu · tab/enter accept a menu row\n")
	sb.WriteString("  /command names match on their letters in order (/rsm finds /resume) · tab completes the argument of /resume, /model, /forget, /undo, /open and /diff\n")
	sb.WriteString("  esc cancel the turn (or clear the input) · ctrl+c cancel, or when idle twice to quit · ctrl+d quit\n")
	sb.WriteString("  pgup/pgdn or mouse wheel scroll · ctrl+o expand the last turn (again folds it) · click a tool line or a thought to fold or unfold it · /verbose does the whole transcript · ctrl+l redraw\n")
	sb.WriteString("  ctrl+g moves a command running in the foreground to the background: it becomes a job and keeps running, and the turn goes on\n")
	sb.WriteString("  typing while the model works queues the message; it is sent when the turn ends · ctrl+s hands it to the running turn instead · up takes the last queued one back · esc cancels and gives them all back\n")
	sb.WriteString("  @path attaches a file, image or directory listing to the prompt · tab completes the path · ctrl+v pastes a clipboard image\n")
	sb.WriteString("  !command runs a shell command here and shares its output with the model\n")
	sb.WriteString("permission prompt: y allow once · a allow this tool for the session · r save a rule so matching calls never ask again · n deny (then type why, or enter to skip) · esc deny · c cancel the whole turn\n")
	all := m.allCommands()
	byName := map[string]command{}
	for _, c := range all {
		byName[c.name] = c
	}
	width := m.width
	if width < 40 {
		width = 80
	}
	const col = 22
	descStyle := lipgloss.NewStyle().Width(width - col - 2)
	seen := map[string]bool{}
	writeGroup := func(title string, names []string) {
		sb.WriteString(title + "\n")
		for _, n := range names {
			c, ok := byName[n]
			if !ok {
				continue
			}
			seen[n] = true
			desc := strings.ReplaceAll(descStyle.Render(c.help), "\n", "\n"+strings.Repeat(" ", col))
			if len(c.name) > 10 || len(c.args) > 8 {
				// Too wide for the columns: the description starts on
				// its own line rather than wrapping from wherever the
				// arguments ended.
				fmt.Fprintf(&sb, "  %s %s\n%s%s\n", c.name, c.args, strings.Repeat(" ", col), desc)
				continue
			}
			fmt.Fprintf(&sb, "  %-10s %-8s %s\n", c.name, c.args, desc)
		}
	}
	for _, g := range helpGroups {
		writeGroup(g.title, g.names)
	}
	var rest []string
	for _, c := range all {
		if !seen[c.name] {
			rest = append(rest, c.name)
		}
	}
	if len(rest) > 0 {
		writeGroup("custom commands and skills", rest)
	}
	sb.WriteString("custom commands: drop prompt templates in .janetcode/commands/<name>.md (or the config dir); $ARGUMENTS is replaced")
	return sb.String()
}

// permissionKeys is the keys line of the permission box, in the longest
// wording that fits on one line. The box has a fixed height in the
// layout, so a wrapped keys line would run over the input.
// permissionKeys is the key line of the permission box. expandable says
// whether there is more of the call to see: without that, the line used to
// offer ctrl+o even for a two-line diff that was already shown whole.
func (m *model) permissionKeys(tool, rule string, expandable bool) string {
	type seg struct{ key, text string }
	// Box width minus its border and padding, with two columns to spare
	// so a line measured at the limit never wraps inside the box.
	room := m.width - 8
	render := func(segs []seg, sep string) (string, int) {
		var sb, plain strings.Builder
		for i, s := range segs {
			if i > 0 {
				sb.WriteString(styleDim.Render(sep))
				plain.WriteString(sep)
			}
			sb.WriteString(styleKey.Render(s.key) + styleDim.Render(s.text))
			plain.WriteString(s.key + s.text)
		}
		return sb.String(), lipgloss.Width(plain.String())
	}
	long := []seg{{"y", " allow once"}, {"a", " always for " + tool + " this session"}, {"r", " save rule " + rule}, {"n", " deny (and say why)"}, {"c", " cancel the turn"}}
	if expandable {
		long = append(long, seg{"", "(ctrl+o expands)"})
	}
	if s, w := render(long, "   "); w <= room {
		return s
	}
	// Shorter labels, and the rule cut to whatever is left.
	fixed := lipgloss.Width("y once · a always for " + tool + " · r rule  · n deny · c cancel")
	if left := room - fixed; left >= 12 && len(rule) > left {
		rule = truncateLine(rule, left)
	}
	medium := []seg{{"y", " once"}, {"a", " always for " + tool}, {"r", " rule " + rule}, {"n", " deny"}, {"c", " cancel"}}
	if s, w := render(medium, " · "); w <= room {
		return s
	}
	s, _ := render([]seg{{"y", " once"}, {"a", " always"}, {"r", " rule"}, {"n", " deny"}, {"c", " cancel"}}, " · ")
	return s
}

// verdictBrief reduces a verdict line ("✓ verified: 2 files edited …")
// to its mark and name for the status bar.
func verdictBrief(line string) string {
	if line == "" {
		return ""
	}
	if i := strings.Index(line, ":"); i > 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// filesChangedLastTurn counts distinct files the last turn changed, from
// the provenance record.
func (m *model) filesChangedLastTurn() int {
	if m.opts.Agent == nil || !m.opts.Agent.LastTurnMutated() {
		return 0
	}
	changes := m.opts.Agent.Changes()
	last := 0
	for _, c := range changes {
		if c.Turn > last {
			last = c.Turn
		}
	}
	files := map[string]bool{}
	for _, c := range changes {
		if c.Turn == last && !c.Undone {
			files[c.Path] = true
		}
	}
	return len(files)
}

// liveWork names background jobs and subagents still running, for the
// status bar; both are otherwise invisible until they finish.
func (m *model) liveWork() string {
	var parts []string
	if m.opts.Jobs != nil {
		n := 0
		for _, j := range m.opts.Jobs.List() {
			if !j.Done {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, noteJob+plural(n, "job")+" (/jobs)")
		}
	}
	if ag := m.opts.Agent; ag != nil && ag.Log != nil {
		if g := agent.ActiveGoal(ag.Log.Events()); g != nil {
			parts = append(parts, fmt.Sprintf("goal %s round %d/%d (/goal pause)", g.ID, g.Rounds, g.MaxRounds))
		}
	}
	if ag := m.opts.Agent; ag != nil && ag.Tools != nil {
		if t, ok := ag.Tools.Get("task"); ok {
			if task, ok := t.(*agent.Task); ok {
				if n := len(task.ChildIDs()); n > 0 {
					parts = append(parts, plural(n, "subagent")+" (/agents)")
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

// helpGroups orders /help by how often a command is reached for; the
// table itself stays in the order the code grew.
var helpGroups = []struct {
	title string
	names []string
}{
	{"everyday", []string{"/help", "/connect", "/model", "/yolo", "/edits", "/undo", "/redo", "/compact", "/clear", "/resume", "/settle", "/quit"}},
	{"reviewing the work", []string{"/open", "/diff", "/changes", "/blame", "/find", "/verbose", "/export", "/usage", "/stats", "/context"}},
	{"steering the session", []string{"/plan", "/readonly", "/goal", "/todos", "/pin", "/unpin", "/fork", "/title", "/history", "/feedback", "/agents", "/jobs"}},
	{"the project", []string{"/memory", "/remember", "/forget", "/skills", "/verify", "/permissions", "/hooks", "/mcp", "/lsp"}},
	{"diagnostics", []string{"/inspect", "/trajectory", "/reload"}},
}

// quitArmWindow is how long a first ctrl+c when idle stays armed.
const quitArmWindow = 2 * time.Second

// strayKeyWindow is how long after a permission prompt closes a lone
// approval key is treated as meant for it.
const strayKeyWindow = 1500 * time.Millisecond

// --- layout & view ---

// compactMode and tight scale the interface to a short window. Neither is
// remembered: they follow the window, and growing it puts everything back.
func (m *model) compactMode() bool { return m.height > 0 && m.height < 20 }
func (m *model) tight() bool       { return m.height > 0 && m.height < 16 }

// menuRows is how many completion rows the menu reserves.
func (m *model) menuRows() int {
	if m.compactMode() {
		return 4
	}
	return menuRows
}

func (m *model) inputHeight() int {
	n := m.input.LineCount()
	if n < minInputLines {
		n = minInputLines
	}
	maxLines := maxInputLines
	if m.compactMode() {
		maxLines = 3
	}
	if n > maxLines {
		n = maxLines
	}
	return n
}

func (m *model) footerHeight() int {
	if m.pending != nil && !m.denying {
		return m.permissionBoxHeight()
	}
	h := m.inputHeight() + 2 + 1 // border, and the reserved notice row
	if len(m.completions()) > 0 {
		// The menu always claims the same height, however many rows it has
		// to show: narrowing it as you type used to resize the viewport on
		// every keystroke, and the transcript flickered above it.
		h += m.menuRows() + 1
	}
	return h
}

// footerNotice is the one reserved row above the input: what the prompt
// is waiting for, or what is queued behind the running turn. It is always
// exactly one line, blank when there is nothing.
func (m *model) footerNotice() string {
	room := m.width - 4
	switch {
	case m.denying && m.pending != nil:
		return "  " + styleWarn.Bold(true).Render("denied "+m.pending.call.Name) + styleDim.Render(" · tell the model why (optional) · enter sends · esc skips")
	case len(m.steering) > 0 && len(m.queued) > 0:
		return fitLine("  "+styleWarn.Render(fmt.Sprintf("steering %d", len(m.steering)))+styleFaint.Render(fmt.Sprintf(" · queued %d · ", len(m.queued)))+styleDim.Render(oneLine(m.queued[len(m.queued)-1])), room)
	case len(m.steering) > 0:
		return fitLine("  "+styleWarn.Render("steering ")+styleDim.Render(oneLine(m.steering[len(m.steering)-1])), room)
	case len(m.queued) > 0:
		return fitLine("  "+styleFaint.Render(fmt.Sprintf("queued %d · ", len(m.queued)))+styleDim.Render(oneLine(m.queued[len(m.queued)-1]))+styleFaint.Render(" · up takes it back"), room)
	}
	return ""
}

// oneLine flattens text for a one-row notice.
func oneLine(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")) }

func (m *model) permissionBoxHeight() int {
	lines := 2 // tool line + keys line
	if m.pending != nil && m.pending.call.Warning != "" {
		lines++
	}
	return lines + 2 // border
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	m.input.SetHeight(m.inputHeight())
	vh := m.height - 1 - 1 - m.footerHeight() // header, status
	if vh < 3 {
		vh = 3
	}
	if !m.ready {
		m.viewport = viewport.New(m.width, vh)
		m.viewport.MouseWheelEnabled = true
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vh
	}
	m.input.SetWidth(m.width - 4)
}

// refresh re-renders the transcript into the viewport, keeping it at the
// bottom while it is pinned (see model.pinned).
func (m *model) refresh(resized bool) {
	if !m.ready {
		return
	}
	content := m.transcript.render(renderOpts{
		cwd:      m.opts.Cwd,
		width:    m.width,
		resized:  resized,
		pending:  m.pending,
		spinner:  m.spinner.View(),
		verbose:  m.verbose,
		cap:      outputCap(m.viewport.Height),
		preview:  m.previewCall,
		renderer: m.toolRenderer,
		tight:    m.tight(),
		compact:  m.compactMode(),
	})
	if content != m.lastContent || resized {
		// SetContent re-parses every line; skip it when nothing changed
		// (spinner ticks with no running tool, key presses, timers).
		m.viewport.SetContent(content)
		m.lastContent = content
	}
	if m.pinned {
		m.viewport.GotoBottom()
	}
}

// scroll passes a key or mouse event to the viewport and re-reads whether
// the transcript still follows new output.
func (m *model) scroll(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.pinned = m.viewport.AtBottom()
	return cmd
}

// repin makes the transcript follow new output again.
func (m *model) repin() {
	m.pinned = true
	m.viewport.GotoBottom()
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.quitting {
		return ""
	}
	body := m.viewport.View()
	switch {
	case m.picker != nil:
		body = m.pickerView(m.viewport.Height)
	case m.connect != nil:
		body = m.connectView(m.viewport.Height)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		body,
		m.statusView(),
		m.footerView(),
	)
}

// headerView is the one-line header: the name and model on the left, the
// mode prefix, working directory and branch on the right. When they do not
// all fit the session title goes first, then the branch, then the path is
// shortened — nothing is ever cut mid-glyph or allowed to wrap.
func (m *model) headerView() string {
	left := styleHeaderHi.Render(" ◆ janetcode ") + styleHeader.Render(m.headerModel())
	title := ""
	if m.opts.Session != nil && m.opts.Session.Meta.Title != "" {
		title = " · " + truncateLine(m.opts.Session.Meta.Title, max(12, m.width/3))
	}
	branch := ""
	if m.branch != "" {
		branch = " ⎇ " + m.branch
	}
	prefix := ""
	switch {
	case m.opts.Agent.PlanMode:
		prefix = "PLAN · "
	case m.opts.Agent.ReadOnly:
		prefix = "READ-ONLY · "
	case m.opts.Agent.AutoApprove:
		prefix = "YOLO · "
	case m.opts.Agent.AutoEdits:
		prefix = "EDITS · "
	}
	// minPath is the shortest useful path: fitPath never goes below it.
	const minPath = 10
	line := func(withTitle, withBranch bool) (string, bool) {
		t, b := "", ""
		if withTitle {
			t = title
		}
		if withBranch {
			b = branch
		}
		fixed := lipgloss.Width(left) + lipgloss.Width(t) + lipgloss.Width(prefix) + lipgloss.Width(b) + 2
		room := m.width - fixed
		if room < minPath {
			return "", false
		}
		p := fitPath(shortPath(m.opts.Cwd), room)
		gap := m.width - fixed - lipgloss.Width(p) + 1
		if gap < 1 {
			return "", false
		}
		return styleHeader.Width(m.width).MaxHeight(1).Render(
			left + styleHeader.Render(t+strings.Repeat(" ", gap)+prefix+p+b+" ")), true
	}
	for _, drop := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		if s, ok := line(drop[0], drop[1]); ok {
			return s
		}
	}
	// Narrower than the name and the mode together: the mode without its
	// separator, and the whole line cut to the width.
	mode := strings.TrimSuffix(prefix, " · ")
	if mode != "" {
		mode = " " + mode
	}
	return styleHeader.Width(m.width).MaxHeight(1).Render(fitLine(left+styleHeader.Render(mode), m.width))
}

// fitPath shows a path in max columns by dropping leading components:
// …/scratchpad/tuiproj rather than the middle of a long directory name,
// which is what a character cut leaves.
func fitPath(p string, max int) string {
	if lipgloss.Width(p) <= max {
		return p
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	for keep := 2; keep >= 1; keep-- {
		if len(parts) <= keep {
			continue
		}
		c := "…/" + strings.Join(parts[len(parts)-keep:], "/")
		if lipgloss.Width(c) <= max {
			return c
		}
	}
	return tailPath(p, max)
}

// tailPath keeps the end of a path so it fits in max columns.
func tailPath(p string, max int) string {
	if max < 8 {
		max = 8
	}
	if lipgloss.Width(p) <= max {
		return p
	}
	r := []rune(p)
	r = r[len(r)-(max-1):]
	if i := strings.IndexRune(string(r), '/'); i >= 0 && i < len(r)-1 {
		r = r[i:]
	}
	return "…" + string(r)
}

func (m *model) statusView() string {
	var left string
	switch {
	case m.pending != nil:
		left = styleWarn.Render("● approval needed")
	case m.running && m.tight():
		left = m.spinner.View() + " " + styleDim.Render(m.phase)
	case m.running:
		el := shortDuration(time.Since(m.startedAt))
		hint := " · esc to cancel"
		// Only while there is something to promote: a key that does
		// nothing is worse than no key.
		if len(m.opts.Running.List()) > 0 {
			hint += " · ctrl+g backgrounds it"
		}
		left = m.spinner.View() + " " + styleDim.Render(m.phase+" · "+el+hint)
	case m.status != "":
		left = styleDim.Render(m.status)
	default:
		left = styleFaint.Render("ready")
	}
	if live := m.liveWork(); live != "" {
		left += styleWarn.Render(" · " + live)
	}
	// The gauge keeps its place whatever else happens. The scroll hint and
	// the cache rate are next to it only while the status text still has
	// room to say something; the cache rate goes first, then the hint.
	parts := []string{}
	if !m.pinned {
		parts = append(parts, styleWarn.Render("↓ pgdn for latest"))
	}
	// In a short window the cache rate is the first thing to go: the
	// scroll hint and the gauge both say something you must act on.
	if pct, ok := m.opts.Agent.CacheRate(); ok && !m.tight() {
		parts = append(parts, styleFaint.Render(fmt.Sprintf("cache %d%%", pct)))
	}
	parts = append(parts, m.contextGauge())
	const leastStatus = 12
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, "  ")) > m.width-leastStatus {
		parts = append(parts[:len(parts)-2], parts[len(parts)-1])
	}
	right := strings.Join(parts, "  ")
	// The status text yields to the gauge, however little is left of it.
	if avail := m.width - lipgloss.Width(right) - 3; lipgloss.Width(left) > avail {
		left = fitLine(left, avail)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return fitLine(" "+left+strings.Repeat(" ", gap)+right+" ", m.width)
}

// contextGauge shows how full the context window is relative to the
// compaction threshold. It is the one thing on the status line that never
// gives way; below 50 columns it drops the bar and keeps the number.
func (m *model) contextGauge() string {
	// While a turn runs the history belongs to the turn goroutine; the
	// gauge reads the estimate the agent published instead.
	est := m.opts.Agent.TokenEstimate()
	if !m.running {
		est = m.opts.Agent.EstimateTokens()
	}
	limit := m.opts.Agent.CompactThreshold()
	if limit <= 0 {
		return styleFaint.Render("ctx " + shortTokens(est))
	}
	pct := est * 100 / limit
	if pct > 100 {
		pct = 100
	}
	style := styleFaint
	switch {
	case pct >= 85:
		style = styleErr
	case pct >= 60:
		style = styleWarn
	}
	if m.width < 50 {
		return style.Render(fmt.Sprintf("ctx %d%%", pct))
	}
	const cells = 10
	filled := pct * cells / 100
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
	return style.Render(fmt.Sprintf("ctx %s %d%%", bar, pct))
}

// inputBox is the border the input box wears in the current state; the
// comment in styles.go says what each colour means.
func (m *model) inputBox() lipgloss.Style {
	switch {
	case m.denying:
		return styleBoxWarn
	case m.running:
		return styleBoxBusy
	}
	return styleBoxIdle
}

// placeholder is the hint shown in an empty input box. It names the same
// states the border colours in styles.go do, and is recomputed every frame
// so no transition can leave a stale hint behind.
func (m *model) placeholder() string {
	switch {
	case m.denying:
		return "Why? the model reads this · enter sends · esc skips"
	case m.opts.Agent.Provider == nil:
		return "No provider yet · /connect picks one"
	case m.running:
		return "Type to queue a message for when this turn ends · esc cancels"
	case m.opts.Agent.PlanMode:
		return "Plan mode: describe the change · /plan approve runs the plan"
	case m.opts.Agent.ReadOnly:
		return "Read-only: the model can inspect but change nothing"
	}
	return defaultPlaceholder
}

func (m *model) footerView() string {
	if m.pending != nil && !m.denying {
		return m.permissionView()
	}
	// One row above the input is always there, blank when there is
	// nothing to say. Notices used to push the prompt down as they
	// arrived and pull it up as they went, so the box moved under the
	// cursor while typing.
	parts := []string{m.footerNotice()}
	// The command menu and the @path menu are the same menu: same rows,
	// same highlight, same footer line, same height whatever is in them.
	if c, idx := m.menu(); len(c) > 0 {
		rows := m.menuRows()
		start := 0
		if idx >= rows {
			start = idx - rows + 1
		}
		end := min(len(c), start+rows)
		for i := start; i < end; i++ {
			cmd := c[i]
			mark, name, help := "  ", styleKey.Render(cmd.name), styleFaint.Render(cmd.help)
			if i == idx {
				mark, name, help = styleAccent.Render("▸ "), styleKey.Bold(true).Render(cmd.name), styleDim.Render(cmd.help)
			}
			parts = append(parts, fitLine(mark+name+" "+styleDim.Render(cmd.args)+"  "+help, m.width-1))
		}
		for i := end - start; i < rows; i++ {
			parts = append(parts, "")
		}
		parts = append(parts, styleFaint.Render(fmt.Sprintf("  ↑↓ choose · %d of %d · tab or enter accepts", idx+1, len(c))))
	}
	box := m.inputBox()
	prompt := styleAccent.Render("❯ ")
	switch {
	case m.denying:
		prompt = styleWarn.Render("❯ ")
	case m.running:
		prompt = styleFaint.Render("❯ ")
	}
	m.input.Placeholder = m.placeholder()
	lines := strings.Split(m.input.View(), "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = prompt + lines[i]
		} else {
			lines[i] = "  " + lines[i]
		}
	}
	parts = append(parts, box.Width(m.width-2).Render(strings.Join(lines, "\n")))
	return strings.Join(parts, "\n")
}

func (m *model) permissionView() string {
	c := m.pending.call
	var sb strings.Builder
	// Everything in this box came from the model or from the repository;
	// none of it may carry control characters into the terminal.
	sb.WriteString(styleWarn.Bold(true).Render("allow ") + styleTool.Bold(true).Render(sanitize(c.Name)) + " " + styleDim.Render(sanitize(summarizeInput(c.Name, string(c.Input), m.width-len(c.Name)-16, m.opts.Cwd))))
	if c.Warning != "" {
		sb.WriteString("\n" + styleErr.Bold(true).Render("⚠ "+sanitize(c.Warning)))
	}
	rule := perm.Suggest(c.Name, perm.Subject(c.Name, c.Input)).String()
	sb.WriteString("\n" + m.permissionKeys(c.Name, rule, pendingExpandable(c)))
	return stylePermBox.Width(m.width - 2).Render(sb.String())
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// undoChange reverts one change from /changes on its own.
func (m *model) undoChange(n int) {
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before undoing")
		return
	}
	done, err := m.opts.Agent.UndoChange(n)
	if err != nil {
		m.transcript.errorf("undo #%d: %v", n, err)
		return
	}
	verb, path, _ := strings.Cut(done, " ")
	if i := strings.LastIndex(done, " "); i > 0 {
		verb, path = done[:i], done[i+1:]
	}
	m.transcript.info(fmt.Sprintf("%s#%d: %s %s", noteUndo, n, verb, m.relPath(path)))
	m.opts.Agent.AddContext(fmt.Sprintf("I undid change #%d (%s) on my own; later changes to the file stayed.", n, m.relPath(path)))
}

// changesCommand lists the session's changes, or one change's hunks.
func (m *model) changesCommand(args []string) {
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			m.transcript.errorf("usage: /changes [n]")
			return
		}
		c, err := m.opts.Agent.Change(n)
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "#%d  %s  %s", c.Seq, c.Tool, m.relPath(c.Path))
		if c.Intent != "" {
			sb.WriteString("  · " + c.Intent)
		}
		if c.Undone {
			sb.WriteString("  (undone)")
		}
		sb.WriteString("\n")
		for _, h := range c.Hunks() {
			sb.WriteString(h.String())
		}
		m.transcript.code(strings.TrimRight(sb.String(), "\n"))
		return
	}
	groups := m.opts.Agent.ChangeGroups()
	if len(groups) == 0 {
		m.transcript.info("no file changes in this session yet")
		return
	}
	var sb strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&sb, "turn %d", g.Turn)
		if g.Intent != "" {
			fmt.Fprintf(&sb, " · %s", g.Intent)
		}
		sb.WriteString("\n")
		for _, c := range g.Changes {
			add, del := c.Stats()
			fmt.Fprintf(&sb, "  #%-3d %-10s %s  +%d -%d", c.Seq, c.Tool, m.relPath(c.Path), add, del)
			if c.Undone {
				sb.WriteString("  (undone)")
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("/changes <n> shows the hunks; /undo <n> reverts one change; /blame <path> attributes lines")
	m.transcript.code(sb.String())
}

// blameCommand attributes each line of a file to the change that wrote it.
func (m *model) blameCommand(path string) {
	if path == "" {
		m.transcript.errorf("usage: /blame <path>")
		return
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(m.opts.Cwd, path)
	}
	lines, note, err := m.opts.Agent.Blame(abs)
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	var sb strings.Builder
	if note != "" {
		sb.WriteString("note: " + note + "\n")
	}
	for i, l := range lines {
		who := "    ·"
		if l.Seq > 0 {
			who = fmt.Sprintf("#%-4d", l.Seq)
		}
		fmt.Fprintf(&sb, "%s %4d │ %s\n", who, i+1, l.Text)
	}
	m.transcript.code(strings.TrimRight(sb.String(), "\n"))
}

// relPath shortens an absolute path under the working directory.
func (m *model) relPath(path string) string {
	if r, err := filepath.Rel(m.opts.Cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		path = r
	}
	return shortPath(path)
}

// undoCommand handles /undo, /undo preview and /redo.
func (m *model) undoCommand(cmd string, preview bool) {
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before %s", strings.TrimPrefix(cmd, "/")+"ing")
		return
	}
	rel := func(path string) string {
		if r, err := filepath.Rel(m.opts.Cwd, path); err == nil && !strings.HasPrefix(r, "..") {
			path = r
		}
		return shortPath(path)
	}
	if preview {
		lines, err := m.opts.Agent.UndoPreview()
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.transcript.errorf("nothing to undo: no turn has changed anything yet")
			return
		}
		if err != nil {
			m.transcript.errorf("undo preview: %v", err)
			return
		}
		if len(lines) == 0 {
			m.transcript.info("/undo would change nothing (the tree already matches the snapshot)")
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "/undo would revert %s:\n", plural(len(lines), "file"))
		for _, l := range lines {
			verb, path, _ := strings.Cut(l, " ")
			fmt.Fprintf(&sb, "  %-8s %s\n", verb, rel(path))
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return
	}
	var (
		done []string
		err  error
		mark = "↶"
	)
	if cmd == "/redo" {
		mark = "↷"
		done, err = m.opts.Agent.Redo()
		if errors.Is(err, agent.ErrNothingToRedo) {
			m.transcript.errorf("nothing to redo: /redo only reapplies an /undo from before the next change")
			return
		}
	} else {
		done, err = m.opts.Agent.Undo()
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.transcript.errorf("nothing to undo: no turn has changed anything yet")
			return
		}
	}
	for _, d := range done {
		verb, path, _ := strings.Cut(d, " ")
		m.transcript.info(mark + " " + verb + " " + rel(path))
	}
	if err != nil {
		m.transcript.errorf("%s incomplete: %v", strings.TrimPrefix(cmd, "/"), err)
	}
	if len(done) == 0 {
		return
	}
	if cmd == "/redo" {
		m.opts.Agent.AddContext("I reapplied the file changes I had undone:\n" + strings.Join(done, "\n"))
		m.status = fmt.Sprintf("redid %s · /undo reverts them again", plural(len(done), "file change"))
		return
	}
	if n := m.opts.Agent.LastUndoRemoved(); n > 0 {
		// The turn's messages left the context with its files; the
		// agent put one note in their place, so nothing more to add.
		m.transcript.info(fmt.Sprintf("%sthe undone turn's %s removed from the model's context as well", noteUndo, plural(n, "message")))
	} else {
		m.opts.Agent.AddContext("I reverted the file changes from your last edits:\n" + strings.Join(done, "\n") + "\nDo not redo them unless I ask.")
	}
	m.status = fmt.Sprintf("undid %s · %s can still be undone · /redo reapplies", plural(len(done), "file change"), plural(len(m.opts.Agent.Checkpoints()), "more turn"))
}

// statsCommand prints cumulative counters for this process: what the
// model did, what it cost and which tools carried the work.
func (m *model) statsCommand() {
	ag := m.opts.Agent
	st := ag.Stats()
	u := ag.UsageSnapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "session %s · up %s · %s · %s", m.opts.Session.Label(), shortDuration(time.Since(m.startedAt)), plural(st.Turns, "turn"), plural(st.ModelCalls, "model call"))
	if st.ModelErrors > 0 {
		fmt.Fprintf(&sb, " (%d failed)", st.ModelErrors)
	}
	if st.ModelCalls > 0 {
		fmt.Fprintf(&sb, " · %s waiting on the model (avg %s)", shortDuration(st.ModelTime), shortDuration(st.ModelTime/time.Duration(st.ModelCalls)))
	}
	fmt.Fprintf(&sb, "\ntokens · in %s · out %s · cache read %s · cache write %s", shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens))
	if pct, ok := ag.CacheRate(); ok {
		fmt.Fprintf(&sb, " · cache hit %d%%", pct)
	}
	if usd, ok := u.Cost(ag.Price); ok {
		sb.WriteString(" · cost " + llm.FormatUSD(usd))
	}
	if st.Recoveries > 0 {
		fmt.Fprintf(&sb, "\n%s recovered from text the model wrote instead of calling", plural(st.Recoveries, "tool call"))
	}
	fmt.Fprintf(&sb, "\ncontext ~%s of %s · %s · %s undoable", shortTokens(ag.EstimateTokens()), tokensOrUnknown(ag.CompactThreshold()), plural(st.Compactions, "compaction"), plural(len(ag.Checkpoints()), "turn"))
	if m.opts.Jobs != nil {
		if jobs := m.opts.Jobs.List(); len(jobs) > 0 {
			running := 0
			for _, j := range jobs {
				if !j.Done {
					running++
				}
			}
			fmt.Fprintf(&sb, " · %s, %d running", plural(len(jobs), "background job"), running)
		}
	}
	calls, errs := st.ToolCalls()
	if calls == 0 {
		sb.WriteString("\nno tool calls yet")
	} else {
		fmt.Fprintf(&sb, "\n%s, %d failed:", plural(calls, "tool call"), errs)
		for _, t := range st.Tools {
			fmt.Fprintf(&sb, "\n  %-14s %10s", t.Name, plural(t.Calls, "call"))
			if t.Errors > 0 {
				fmt.Fprintf(&sb, "  %d failed", t.Errors)
			}
			fmt.Fprintf(&sb, "  %s total", shortDuration(t.Time))
		}
	}
	m.transcript.info(sb.String())
}

func tokensOrUnknown(n int) string {
	if n <= 0 {
		return "no limit"
	}
	return shortTokens(n) + " compact threshold"
}

// mcpAuth handles /mcp auth <server> and /mcp logout <server>.
func (m *model) mcpAuth(args []string) {
	o := m.opts.MCP
	if len(args) != 2 {
		m.transcript.errorf("usage: /mcp %s <server>", args[0])
		return
	}
	name := args[1]
	cfg, ok := o.Global[name]
	if !ok {
		cfg, ok = o.Project[name]
	}
	switch {
	case !ok:
		m.transcript.errorf("no MCP server named %q in the config or %s", name, mcp.ProjectFile)
		return
	case cfg.URL == "":
		m.transcript.errorf("mcp %s runs locally (a command); only remote servers use OAuth", name)
		return
	case o.Tokens == nil:
		m.transcript.errorf("OAuth is not available in this session (no token store)")
		return
	}
	if args[0] == "logout" {
		if _, had := o.Tokens.Get(cfg.URL); !had {
			m.transcript.info("mcp " + name + ": no token stored")
			return
		}
		if err := o.Tokens.Delete(cfg.URL); err != nil {
			m.transcript.errorf("forget token: %v", err)
			return
		}
		m.transcript.info("mcp " + name + ": token forgotten · the server will need /mcp auth again")
		return
	}
	// A failed start may have told us where the server's metadata lives.
	var meta string
	for _, st := range o.Manager.Status() {
		var ae *mcp.AuthError
		if st.Name == name && errors.As(st.Err, &ae) {
			meta = ae.ResourceMetadata
		}
	}
	openURL := o.OpenURL
	if openURL == nil {
		openURL = mcp.OpenBrowser
	}
	m.transcript.info("mcp " + name + ": starting authorization · the browser will open; the URL is also printed here")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, err := mcp.Authorize(ctx, mcp.AuthOptions{
			ServerURL: cfg.URL, Store: o.Tokens, ResourceMetadata: meta,
			ClientID: cfg.ClientID, ClientSecret: os.ExpandEnv(cfg.ClientSecret), Scope: cfg.Scope,
			OpenURL: openURL,
			Notify:  func(s string) { m.send(mcpAuthMsg{name: name, text: s}) },
		})
		m.send(mcpAuthMsg{name: name, done: true, err: err})
	}()
}

// restartMCP stops a server and starts it again with the current tokens.
func (m *model) restartMCP(name string) {
	o := m.opts.MCP
	cfg, ok := o.Global[name]
	if !ok {
		if cfg, ok = o.Project[name]; !ok || !o.ProjectTrusted {
			return
		}
	}
	o.Manager.Stop(name)
	o.Manager.Start(context.Background(), name, cfg, func(name string, c *mcp.Client, err error) {
		n := 0
		if c != nil {
			n = len(c.Tools())
		}
		m.send(mcpDoneMsg{name: name, tools: n, err: err})
	})
}

// clickTranscript toggles the entry drawn on the clicked row. It reports
// whether the click landed on one; anything else falls through to the
// viewport, which reads clicks as the start of a drag-scroll.
func (m *model) clickTranscript(y int) bool {
	if !m.ready || m.connect != nil {
		return false
	}
	// The header owns row 0; the viewport starts under it.
	row := y - 1
	if row < 0 || row >= m.viewport.Height {
		return false
	}
	e := m.transcript.entryAtLine(m.viewport.YOffset + row)
	if e == nil {
		return false
	}
	e.expanded, e.final = !e.expanded, false
	if e.expanded {
		m.status = "expanded · click again to fold"
	} else {
		m.status = "folded"
	}
	m.refresh(false)
	return true
}

// A paste of any size arrives as one bracketed-paste key event. Over ten
// lines or a thousand characters it is kept aside and shown as a chip,
// so the prompt stays readable and a paste that ends in a newline cannot
// send itself.
const (
	pasteChipLines = 10
	pasteChipChars = 1000
)

var pasteMarkerRe = regexp.MustCompile(`\[paste #(\d+) \+\d+ lines?\]`)

// bigPaste reports whether a paste is large enough to be worth a chip.
func bigPaste(s string) bool {
	return strings.Count(s, "\n") >= pasteChipLines || len(s) > pasteChipChars
}

// storePaste keeps the text and returns the chip that stands for it.
func (m *model) storePaste(text string) string {
	m.pastes = append(m.pastes, text)
	return fmt.Sprintf("[paste #%d +%s]", len(m.pastes), plural(strings.Count(text, "\n")+1, "line"))
}

// inflatePastes puts the pasted text back in place of every chip.
func (m *model) inflatePastes(text string) string {
	if len(m.pastes) == 0 {
		return text
	}
	return pasteMarkerRe.ReplaceAllStringFunc(text, func(s string) string {
		n, err := strconv.Atoi(pasteMarkerRe.FindStringSubmatch(s)[1])
		if err != nil || n < 1 || n > len(m.pastes) {
			return s
		}
		return m.pastes[n-1]
	})
}

// restoreQueued puts queued messages back in the input, under whatever is
// already there, separated by blank lines. Nothing typed is ever thrown
// away by a cancel: it goes back where it came from.
func (m *model) restoreQueued() {
	if len(m.queued) == 0 {
		return
	}
	parts := m.queued
	m.queued = nil
	if v := strings.TrimSpace(m.input.Value()); v != "" {
		parts = append(parts, v)
	}
	m.input.SetValue(strings.Join(parts, "\n\n"))
	m.input.CursorEnd()
	m.layout()
	m.refresh(false)
}

// fuzzyCommand matches a command name against what has been typed so far
// as a subsequence, so "/mcpa" finds /mcp__auth and "/rsm" finds /resume.
// The query is split on "/" first: the separator in a namespaced command
// is part of its shape, not a letter to match.
func fuzzyCommand(name, query string) bool {
	n := strings.ToLower(strings.TrimPrefix(name, "/"))
	for _, part := range strings.Split(strings.ToLower(strings.TrimPrefix(query, "/")), "/") {
		if part == "" {
			continue
		}
		i := 0
		for _, r := range part {
			j := strings.IndexRune(n[i:], r)
			if j < 0 {
				return false
			}
			i += j + 1
		}
	}
	return query != "/"
}

// --- argument completion ---
//
// Where a command's argument comes from a list the harness already has —
// the sessions here, the models configured, what is remembered, the
// changes this session made, the files in the tree — tab offers it rather
// than making the user go and look it up.

func (m *model) completeSession(partial string) []command {
	if m.opts.Session == nil {
		return nil
	}
	infos, err := session.List(dirOf(m.opts.Session), m.opts.Cwd)
	if err != nil {
		return nil
	}
	var out []command
	n := 0
	for _, in := range infos {
		if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
			continue
		}
		n++
		name := "/resume " + in.Meta.ID
		if partial != "" && !strings.Contains(strings.ToLower(in.Meta.ID+" "+in.Label()), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: name, args: shortAge(time.Since(in.Updated)), help: plural(in.Messages, "msg") + " · " + in.Label()})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func (m *model) completeModel(partial string) []command {
	if m.opts.Config == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []command
	for _, n := range m.opts.Config.ProviderNames() {
		p := m.opts.Config.Providers[n]
		if p.Model == "" || seen[p.Model] {
			continue
		}
		seen[p.Model] = true
		if partial != "" && !strings.Contains(strings.ToLower(p.Model), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/model " + p.Model, help: "configured for " + n})
	}
	return out
}

func (m *model) completeMemory(partial string) []command {
	if m.opts.Memory == nil {
		return nil
	}
	var out []command
	for _, e := range m.opts.Memory.Entries() {
		if partial != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/forget " + e.Name, help: e.Description})
	}
	return out
}

func (m *model) completeUndo(partial string) []command {
	out := []command{{name: "/undo preview", help: "list what would be reverted"}}
	if m.opts.Agent == nil {
		return out
	}
	for i, c := range m.opts.Agent.Changes() {
		if c.Undone {
			continue
		}
		out = append(out, command{name: fmt.Sprintf("/undo %d", i+1), help: m.relPath(c.Path)})
	}
	if partial == "" {
		return out
	}
	var hit []command
	for _, c := range out {
		if strings.Contains(c.name+" "+c.help, partial) {
			hit = append(hit, c)
		}
	}
	return hit
}

// completeFile offers paths for the commands that take one.
func (m *model) completeFile(partial string) []command {
	name := "/open "
	if strings.HasPrefix(m.input.Value(), "/diff") {
		name = "/diff "
	}
	var out []command
	for _, p := range prompt.CompletePath(m.opts.Cwd, partial) {
		help := "file"
		if strings.HasSuffix(p, "/") {
			help = "directory"
		}
		out = append(out, command{name: name + p, help: help})
	}
	return out
}

// previewCall asks a tool what a call would do, for a call whose
// arguments have finished streaming but which has not run yet. The edit
// tools answer with a unified diff against the file on disk; anything
// else, and any error, answers with nothing.
func (m *model) previewCall(name string, input []byte) string {
	if m.opts.Agent == nil || m.opts.Agent.Tools == nil {
		return ""
	}
	t, ok := m.opts.Agent.Tools.Get(name)
	if !ok {
		return ""
	}
	pv, ok := t.(tool.Previewer)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := pv.Preview(ctx, input)
	if err != nil {
		return ""
	}
	return out
}

// toolRenderer finds the tool behind a call when it knows how to present
// itself in the transcript (see tool.Renderer). MCP and user-supplied
// tools do not, and get the generic rendering.
func (m *model) toolRenderer(name string) (tool.Renderer, bool) {
	if m.opts.Agent == nil || m.opts.Agent.Tools == nil {
		return nil, false
	}
	t, ok := m.opts.Agent.Tools.Get(name)
	if !ok {
		return nil, false
	}
	r, ok := t.(tool.Renderer)
	return r, ok
}

// reportCacheWaste says when a turn paid a second time for a prefix the
// turn before had cached. It is invisible otherwise: the turn works, the
// answer is right, and the bill is several times larger.
func (m *model) reportCacheWaste() {
	n, cause, idle := m.opts.Agent.TurnCacheWaste()
	if n == 0 {
		return
	}
	head := "cache miss"
	if idle > time.Minute {
		head += " after " + shortDuration(idle.Round(time.Minute)) + " idle"
	}
	line := fmt.Sprintf("%s: %s tokens re-billed", head, shortTokens(n))
	if usd, ok := (llm.Usage{InputTokens: n}).Cost(m.opts.Agent.Price); ok {
		line += " (~" + llm.FormatUSD(usd) + ")"
	}
	m.transcript.info(noteNotice + line + " · " + cause)
}
