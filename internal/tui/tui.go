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
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Options configures the UI.
type Options struct {
	// ConfigDir holds global custom commands under commands/*.md.
	ConfigDir string
	// MCP, when set, describes the servers to start.
	MCP      *MCPOptions
	Agent    *agent.Agent
	Session  *session.Session
	Provider string
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
}

// Run starts the interactive UI and blocks until the user quits.
func Run(opts Options) error {
	m := newModel(opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
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
type flushMsg struct{}
type agentDoneMsg struct {
	err     error
	syncErr error
}
type sessionSwapMsg struct{ sess *session.Session }
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

type command struct {
	name, args, help string
}

var commands = []command{
	{"/help", "", "keys and commands"},
	{"/compact", "", "summarise the conversation to free context"},
	{"/clear", "", "start a new session"},
	{"/resume", "[n|id]", "list earlier sessions here, or reopen one"},
	{"/find", "<text>", "jump to the next line matching text; repeat to continue"},
	{"/undo", "", "revert the files changed by the last turn"},
	{"/mcp", "[trust]", "MCP servers and their tools; trust starts the project's .mcp.json servers"},
	{"/yolo", "", "toggle auto-approve for tools"},
	{"/connect", "", "connect to a provider (preset or custom API) and pick a model"},
	{"/model", "[name]", "pick a model from a list, or set one by name"},
	{"/usage", "", "tokens and cost for this session"},
	{"/verbose", "", "toggle expanded tool output (ctrl+o)"},
	{"/quit", "", "exit"},
}

// --- model ---

type model struct {
	opts     Options
	approver *tuiApprover
	// send delivers messages from background goroutines into the loop.
	send func(tea.Msg)

	transcript *transcript
	queue      *eventQueue
	viewport   viewport.Model
	input      textarea.Model
	spinner    spinner.Model

	width, height int
	ready         bool
	verbose       bool
	lastContent   string // last transcript pushed into the viewport

	// history of submitted prompts for up/down recall.
	history []string
	histIdx int    // == len(history) when not browsing
	draft   string // input saved while browsing history

	running    bool
	cancelling bool
	compacting bool
	cancel     context.CancelFunc
	startedAt  time.Time
	phase      string // what the agent is doing right now
	pending    *permissionMsg
	denying    bool // the input box is collecting a reason for a denial
	status     string
	lastTurn   string                   // usage summary of the last turn
	queued     []string                 // prompts typed while a turn was running
	custom     map[string]prompt.Custom // user-defined slash commands
	clipboard  func() ([]byte, error)   // reads a PNG from the clipboard; swapped in tests
	compIdx    int                      // highlighted row of the completion menu
	compFor    string                   // input value the highlight belongs to
	lastFind   string                   // last /find query, for repeats
	lastFindAt int                      // line of the last hit, so repeats advance past it
	connect    *connect                 // open provider/model dialog
	connectGen int
	quitting   bool
	branch     string
}

const (
	minInputLines = 1
	maxInputLines = 6

	defaultPlaceholder = "Ask for a change, a fix, or an explanation…  (/ for commands)"
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

	// Detect the background once, before the program starts: the query
	// talks to the terminal and must not race with key input later.
	style := opts.Theme
	if style != "dark" && style != "light" {
		style = "light"
		if lipgloss.HasDarkBackground() {
			style = "dark"
		}
	}
	m := &model{
		opts:       opts,
		approver:   &tuiApprover{},
		transcript: newTranscriptStyled(style),
		queue:      &eventQueue{},
		input:      ta,
		spinner:    sp,
	}
	if b, ok := prompt.GitBranch(opts.Cwd); ok {
		m.branch = b
		if b == "" {
			m.branch = "git"
		}
	}
	m.clipboard = readClipboardImage
	m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(opts.Cwd, opts.ConfigDir) {
		m.custom["/"+c.Name] = c
	}
	m.transcript.banner(m.welcome())
	if opts.Resumed > 0 {
		m.transcript.info(fmt.Sprintf("resumed session %s · %d messages", opts.Session.Meta.ID, opts.Resumed))
		m.transcript.loadHistory(opts.Agent.Messages)
	}
	return m
}

func (m *model) welcome() string {
	var sb strings.Builder
	if m.opts.Agent.Provider == nil {
		fmt.Fprintf(&sb, "janetcode %s · no provider configured yet: pick one below, or set ANTHROPIC_API_KEY / OPENAI_API_KEY\n", m.opts.Version)
	} else {
		fmt.Fprintf(&sb, "janetcode %s · %s · %s\n", m.opts.Version, m.opts.Provider, m.opts.Agent.Model)
	}
	fmt.Fprintf(&sb, "working in %s", tailPath(shortPath(m.opts.Cwd), 70))
	if m.branch != "" {
		fmt.Fprintf(&sb, " on %s", m.branch)
	}
	sb.WriteString("\n")
	if m.opts.Agent.AutoApprove {
		sb.WriteString("auto-approve is ON: tools run without asking\n")
	} else {
		sb.WriteString("edits and commands ask first and show a diff · y allow · a always · n deny\n")
	}
	sb.WriteString("enter sends · ctrl+j newline · esc cancels · /help for more")
	return sb.String()
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
			names = append(names, n)
		}
		sort.Strings(names)
		m.transcript.info(fmt.Sprintf("%s lists MCP servers (%s) that have not been trusted for this directory. They would run commands on your machine: inspect the file, then /mcp trust to start them.", mcp.ProjectFile, strings.Join(names, ", ")))
	}
	if n := len(o.Global) + len(o.Project); n > 0 && (o.ProjectTrusted || len(o.Global) > 0) {
		m.status = "starting MCP servers…"
	}
	m.refresh(false)
}

func (m *model) Init() tea.Cmd {
	m.startMCP()
	return tea.Batch(textarea.Blink, m.spinner.Tick, tick())
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
			m.history = append(m.history, text)
			m.histIdx = len(m.history)
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

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tickMsg:
		// Keeps elapsed timers and throttled live markdown moving even when
		// no event arrives.
		if m.running {
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

	case sessionSwapMsg:
		m.opts.Session = msg.sess
		m.opts.Agent.UndoPath = msg.sess.UndoPath()
		m.opts.Agent.SaveUndo()
		m.transcript.info("continuing in session " + msg.sess.Meta.ID)
		m.refresh(false)
		return m, nil

	case permissionMsg:
		m.drainEvents() // the tool entry must exist before the prompt shows
		m.pending = &msg
		m.phase = "waiting for your approval"
		m.refresh(false)
		m.viewport.GotoBottom()
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

	case mcpDoneMsg:
		if msg.err != nil {
			m.transcript.errorf("mcp %s: %v", msg.name, msg.err)
		} else {
			line := fmt.Sprintf("mcp %s ready · %d tools", msg.name, msg.tools)
			if n := len(m.mcpPrompts(msg.name)); n > 0 {
				line += fmt.Sprintf(" · %d prompts as /mcp__%s__<name>", n, msg.name)
			}
			m.transcript.info(line + " (see /mcp)")
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
		m.refresh(false)
		return m, cmd
	}

	var cmd tea.Cmd
	before := m.input.LineCount()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	if m.input.LineCount() != before {
		m.layout()
		m.refresh(false)
	}
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
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
	case agent.EvToolRun:
		m.phase = "running " + ev.Call.Name
	case agent.EvToolResult:
		m.phase = "thinking"
	case agent.EvCompacted:
		m.phase = "thinking"
	case agent.EvTurnDone:
		u := ev.Usage
		s := fmt.Sprintf("%s in · %s out", shortTokens(u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens), shortTokens(u.OutputTokens))
		if u.CacheReadTokens > 0 {
			s += fmt.Sprintf(" · %s cached", shortTokens(u.CacheReadTokens))
		}
		if usd, ok := m.opts.Agent.Usage.Cost(m.opts.Agent.Price); ok {
			s += " · " + llm.FormatUSD(usd) + " total"
		}
		if ev.StopReason != "" && ev.StopReason != "end_turn" {
			s += " · stopped: " + string(ev.StopReason)
		}
		m.lastTurn = s
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.connect != nil {
		return m.handleConnectKey(msg)
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
		case "n", "N":
			// Ask why so the model can adapt; enter with nothing skips.
			m.denying = true
			m.input.Reset()
			m.input.Placeholder = "why? the model reads this (optional)"
			m.phase = "waiting for your reason"
			m.layout()
			m.refresh(false)
		case "esc":
			m.answer(agent.Deny, "")
		case "ctrl+c":
			m.answer(agent.Deny, "")
			m.cancelTurn()
		case "ctrl+o":
			m.verbose = !m.verbose
			m.refresh(false)
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return cmd, true
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
			m.layout()
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
			return nil, true
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.layout()
			m.refresh(false)
			return nil, true
		}
	case "ctrl+o":
		m.verbose = !m.verbose
		if m.verbose {
			m.status = "verbose tool output on"
		} else {
			m.status = "verbose tool output off"
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
		if m.input.Line() == 0 && len(m.history) > 0 && m.histIdx > 0 {
			if m.histIdx == len(m.history) {
				m.draft = m.input.Value()
			}
			m.histIdx--
			m.input.SetValue(m.history[m.histIdx])
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
				m.input.SetValue(m.history[m.histIdx])
			}
			m.input.CursorEnd()
			m.layout()
			return nil, true
		}
	case "pgup", "pgdown", "ctrl+u", "ctrl+b", "ctrl+f":
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return cmd, true
	case "enter":
		if c, idx := m.menu(); len(c) > 0 && strings.TrimSpace(m.input.Value()) != c[idx].name {
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

// remember adds a submitted prompt to the up/down history.
func (m *model) remember(text string) {
	if n := len(m.history); n == 0 || m.history[n-1] != text {
		m.history = append(m.history, text)
	}
	m.histIdx = len(m.history)
	m.draft = ""
}

// completions lists slash commands matching the input, or files matching
// the @path being typed at its end.
func (m *model) completions() []command {
	if m.connect != nil {
		return nil
	}
	v := m.input.Value()
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " \n") {
		var out []command
		for _, c := range m.allCommands() {
			if strings.HasPrefix(c.name, v) {
				out = append(out, c)
			}
		}
		return out
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
		out = append(out, command{name: n, args: "[args]", help: c.Help + " · " + shortPath(c.Path)})
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
				help := strings.TrimSpace(p.Description)
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
	v := m.input.Value()
	if strings.HasPrefix(v, "/") {
		m.input.SetValue(c[idx].name + " ")
	} else {
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
	}
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
	m.denying = false
	m.input.Placeholder = defaultPlaceholder
	m.phase = "thinking"
	m.refresh(false)
}

func (m *model) submit(text string) tea.Cmd {
	if m.opts.Agent.Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect")
		m.refresh(false)
		return nil
	}
	m.transcript.user(text)
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
		m.transcript.info("attached " + strings.Join(names, " · "))
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: text}}, prompt.ImageBlocks(atts)...)
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, sess, send, queue := m.opts.Agent, m.opts.Session, m.send, m.queue
	m.refresh(false)
	m.viewport.GotoBottom()
	go func() {
		err := ag.RunBlocks(ctx, blocks, queue.push)
		ns, syncErr := sess.SyncOrRotate(ag.Messages)
		if ns != sess {
			send(sessionSwapMsg{sess: ns})
		}
		send(agentDoneMsg{err: err, syncErr: syncErr})
	}()
	return m.spinner.Tick
}

// shell runs a command the user typed with a "!" prefix in the project
// directory, shows it like a tool call, and hands the output to the model
// as context for the next turn.
func (m *model) shell(cmdline string) tea.Cmd {
	if cmdline == "" {
		m.transcript.errorf("usage: !<command>  (runs in %s and shares the output with the model)", shortPath(m.opts.Cwd))
		m.refresh(false)
		return nil
	}
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before running a command")
		m.refresh(false)
		return nil
	}
	input, _ := json.Marshal(map[string]string{"command": cmdline})
	call := agent.ToolCall{ID: fmt.Sprintf("shell-%d", time.Now().UnixNano()), Name: "shell", Input: input}
	m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	m.startTurn("running your command")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	send, cwd := m.send, m.opts.Cwd
	m.refresh(false)
	m.viewport.GotoBottom()
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
	m.running = true
	m.cancelling = false
	m.startedAt = time.Now()
	m.status = ""
	m.phase = phase
}

// compact summarises the history in the background like a turn.
func (m *model) compact() tea.Cmd {
	m.startTurn("compacting context")
	m.compacting = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, sess, send, queue := m.opts.Agent, m.opts.Session, m.send, m.queue
	m.transcript.info(fmt.Sprintf("compacting ~%s tokens of context…", shortTokens(ag.EstimateTokens())))
	m.refresh(false)
	go func() {
		err := ag.Compact(ctx, queue.push)
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		var syncErr error
		if err == nil {
			// Compaction rewrote the history; the old transcript stays on
			// disk and a new session file takes over.
			var ns *session.Session
			if ns, syncErr = sess.SyncOrRotate(ag.Messages); ns != sess {
				send(sessionSwapMsg{sess: ns})
			}
		}
		send(agentDoneMsg{err: err, syncErr: syncErr})
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
	m.phase = ""
	elapsed := shortDuration(time.Since(m.startedAt))
	m.transcript.finishTurn()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.transcript.dropEmptyTrailing()
		m.transcript.errorf("cancelled after %s", elapsed)
		m.status = "cancelled"
	case errors.Is(msg.err, agent.ErrMaxIterations):
		m.transcript.errorf("stopped after %d tool rounds in one turn. Send a message to continue.", m.opts.Agent.MaxIterations)
		m.status = "stopped"
	case msg.err != nil:
		m.transcript.dropEmptyTrailing()
		m.transcript.errorf("%s", friendlyError(msg.err, m.opts.Provider))
		m.status = "error"
	case compacting:
		m.status = "compacted in " + elapsed
	default:
		m.status = "done in " + elapsed
		if m.lastTurn != "" {
			m.status += " · " + m.lastTurn
		}
	}
	if msg.syncErr != nil {
		m.transcript.errorf("could not save session: %v", msg.syncErr)
	}
	if len(m.queued) == 0 {
		return nil
	}
	if msg.err != nil {
		// Do not fire queued prompts into a failed or cancelled turn; hand
		// them back so nothing typed is lost.
		text := strings.Join(m.queued, "\n")
		m.queued = nil
		if m.input.Value() == "" {
			m.input.SetValue(text)
			m.input.CursorEnd()
			m.layout()
		}
		return nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	return m.submit(next)
}

// friendlyError turns provider failures into something actionable.
func friendlyError(err error, provider string) string {
	s := err.Error()
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "connection refused") || strings.Contains(l, "no such host") || strings.Contains(l, "dial tcp"):
		return "cannot reach the API: " + s + "\n  check the base URL for provider " + provider + " (/connect) and your network"
	case strings.Contains(l, "http 401") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		return "authentication failed: " + s + "\n  check the API key for provider " + provider + " (/connect)"
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		return "rate limited or out of quota: " + s + "\n  wait and resend, add keys with /connect, or switch model with /model"
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"):
		return "model not available: " + s + "\n  pick another with /model"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "the request timed out: " + s
	}
	return s
}

func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
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
		m.opts.Agent.UndoPath = sess.UndoPath()
		m.transcript = newTranscriptStyled(m.transcript.styleName)
		m.transcript.banner(m.welcome())
		m.transcript.info("new session " + sess.Meta.ID)
	case "/yolo":
		m.opts.Agent.AutoApprove = !m.opts.Agent.AutoApprove
		if m.opts.Agent.AutoApprove {
			m.transcript.info("auto-approve ON: tools run without asking")
		} else {
			m.transcript.info("auto-approve OFF: edits and commands ask first")
		}
	case "/verbose":
		m.verbose = !m.verbose
		m.transcript.info(fmt.Sprintf("verbose tool output: %v", m.verbose))
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
		m.opts.Agent.Model = fields[1]
		if cfg := m.opts.Config; cfg != nil {
			cfg.Model = fields[1]
			m.opts.Agent.Price = cfg.PriceFor(fields[1])
			if m.opts.ConfigPath != "" {
				if err := config.Update(m.opts.ConfigPath, cfg.Provider, cfg.Active(), fields[1]); err != nil {
					m.transcript.errorf("saving config: %v", err)
				}
			}
		}
		m.transcript.info("model set to " + fields[1] + " on " + m.opts.Provider)
	case "/mcp":
		m.mcpCommand(fields[1:])
	case "/undo":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before undoing")
			break
		}
		done, err := m.opts.Agent.Undo()
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.transcript.errorf("nothing to undo: no turn has changed files through edit_file or write_file (shell changes are not tracked)")
			break
		}
		for _, d := range done {
			m.transcript.info("↶ " + shortPath(d))
		}
		if err != nil {
			m.transcript.errorf("undo incomplete: %v", err)
		}
		if len(done) > 0 {
			m.opts.Agent.AddContext("I reverted the file changes from your last edits:\n" + strings.Join(done, "\n") + "\nDo not redo them unless I ask.")
			m.status = fmt.Sprintf("undid %d file change(s) · %d more turn(s) can be undone", len(done), len(m.opts.Agent.Checkpoints()))
		}
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
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before switching sessions")
			break
		}
		if len(fields) < 2 {
			m.listSessions()
			break
		}
		m.resume(fields[1])
	case "/compact":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before compacting")
			break
		}
		return m.compact()
	case "/usage", "/cost":
		u := m.opts.Agent.Usage
		line := fmt.Sprintf("session tokens · in %s · out %s · cache read %s · cache write %s · context now ~%s",
			shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens), shortTokens(m.opts.Agent.EstimateTokens()))
		switch usd, ok := u.Cost(m.opts.Agent.Price); {
		case ok && u.CostUSD > 0:
			line += "\ncost " + llm.FormatUSD(usd) + " (reported by the provider)"
		case ok:
			line += "\ncost " + llm.FormatUSD(usd) + " (estimated from prices in the config file)"
		default:
			line += "\ncost unknown: the provider does not report it; add the model under \"prices\" in the config file to estimate"
		}
		m.transcript.info(line)
	default:
		if c, ok := m.custom[fields[0]]; ok {
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			return m.submit(c.Expand(args))
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
	m.status = fmt.Sprintf("match %d/%d for %q on line %d · /find again for the next", idx+1, len(hits), q, next+1)
}

func (m *model) mcpCommand(args []string) {
	o := m.opts.MCP
	if o == nil || o.Manager == nil {
		m.transcript.info("MCP is not configured: add servers under \"mcp_servers\" in the config file or a .mcp.json in the project")
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

// listSessions shows the sessions recorded for this directory, numbered so
// "/resume 2" can pick one.
func (m *model) listSessions() {
	infos, err := session.List(dirOf(m.opts.Session), m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("list sessions: %v", err)
		return
	}
	var sb strings.Builder
	n := 0
	for _, in := range infos {
		if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
			continue
		}
		n++
		if n > 15 {
			sb.WriteString(fmt.Sprintf("  … %d more (janetcode --sessions lists all)\n", len(infos)-n+1))
			break
		}
		age := shortAge(time.Since(in.Meta.Created))
		fmt.Fprintf(&sb, "  %2d  %s  %-6s %3d msgs  %s\n", n, in.Meta.ID, age, in.Messages, in.Preview)
	}
	if n == 0 {
		m.transcript.info("no earlier sessions in " + shortPath(m.opts.Cwd))
		return
	}
	m.transcript.info("sessions here (newest first) · /resume <n> or /resume <id> reopens one\n" + strings.TrimRight(sb.String(), "\n"))
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
	m.opts.Agent.Usage = llm.Usage{}
	m.opts.Agent.UndoPath = sess.UndoPath()
	if err := m.opts.Agent.LoadUndo(); err != nil {
		m.transcript.errorf("%v", err)
	}
	m.transcript = newTranscriptStyled(m.transcript.styleName)
	m.transcript.banner(m.welcome())
	m.transcript.info(fmt.Sprintf("resumed session %s · %d messages", sess.Meta.ID, len(history)))
	m.transcript.loadHistory(history)
	m.lastTurn = ""
	m.viewport.GotoBottom()
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
	sb.WriteString("  esc cancel the turn (or clear the input) · ctrl+c cancel, twice to force quit · ctrl+d quit\n")
	sb.WriteString("  pgup/pgdn or mouse wheel scroll · ctrl+o expand tool output · ctrl+l redraw\n")
	sb.WriteString("  typing while the model works queues the message; it is sent when the turn ends\n")
	sb.WriteString("  @path attaches a file, image or directory listing to the prompt · tab completes the path · ctrl+v pastes a clipboard image\n")
	sb.WriteString("  !command runs a shell command here and shares its output with the model\n")
	sb.WriteString("permission prompt: y allow once · a allow this tool for the session · n deny (then type why, or enter to skip) · esc deny\n")
	sb.WriteString("commands\n")
	for _, c := range m.allCommands() {
		fmt.Fprintf(&sb, "  %-10s %-7s %s\n", c.name, c.args, c.help)
	}
	sb.WriteString("custom commands: drop prompt templates in .janetcode/commands/<name>.md (or the config dir); $ARGUMENTS is replaced")
	return sb.String()
}

// --- layout & view ---

func (m *model) inputHeight() int {
	n := m.input.LineCount()
	if n < minInputLines {
		n = minInputLines
	}
	if n > maxInputLines {
		n = maxInputLines
	}
	return n
}

func (m *model) footerHeight() int {
	if m.pending != nil && !m.denying {
		return m.permissionBoxHeight()
	}
	h := m.inputHeight() + 2 // border
	if c := m.completions(); len(c) > 0 {
		h += min(len(c), menuRows)
		if len(c) > menuRows {
			h++
		}
	}
	if m.denying {
		h++
	}
	return h + len(m.queued)
}

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

// refresh re-renders the transcript into the viewport. When the viewport was
// at the bottom it stays pinned there so streaming output scrolls.
func (m *model) refresh(resized bool) {
	if !m.ready {
		return
	}
	atBottom := m.viewport.AtBottom()
	content := m.transcript.render(renderOpts{
		width:   m.width,
		resized: resized,
		pending: m.pending,
		spinner: m.spinner.View(),
		verbose: m.verbose,
	})
	if content != m.lastContent || resized {
		// SetContent re-parses every line; skip it when nothing changed
		// (spinner ticks with no running tool, key presses, timers).
		m.viewport.SetContent(content)
		m.lastContent = content
	}
	if atBottom || resized {
		m.viewport.GotoBottom()
	}
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.quitting {
		return ""
	}
	body := m.viewport.View()
	if m.connect != nil {
		body = m.connectView(m.viewport.Height)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		body,
		m.statusView(),
		m.footerView(),
	)
}

func (m *model) headerView() string {
	left := styleHeaderHi.Render(" ◆ janetcode ") + styleHeader.Render(m.headerModel())
	suffix := ""
	if m.branch != "" {
		suffix = " ⎇ " + m.branch
	}
	prefix := ""
	if m.opts.Agent.AutoApprove {
		prefix = "YOLO · "
	}
	avail := m.width - lipgloss.Width(left) - lipgloss.Width(prefix) - lipgloss.Width(suffix) - 3
	right := styleHeader.Render(prefix + tailPath(shortPath(m.opts.Cwd), avail) + suffix + " ")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + styleHeader.Render(strings.Repeat(" ", gap)) + right
	return styleHeader.Width(m.width).MaxHeight(1).Render(line)
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
	case m.running:
		el := shortDuration(time.Since(m.startedAt))
		left = m.spinner.View() + " " + styleDim.Render(m.phase+" · "+el+" · esc to cancel")
	case m.status != "":
		left = styleDim.Render(m.status)
	default:
		left = styleFaint.Render("ready")
	}
	right := m.contextGauge()
	if !m.viewport.AtBottom() {
		right = styleWarn.Render("↓ pgdn for latest") + "  " + right
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// contextGauge shows how full the context window is relative to the
// compaction threshold.
func (m *model) contextGauge() string {
	est := m.opts.Agent.EstimateTokens()
	limit := m.opts.Agent.CompactThreshold()
	if limit <= 0 {
		return styleFaint.Render("ctx " + shortTokens(est))
	}
	pct := est * 100 / limit
	if pct > 100 {
		pct = 100
	}
	const cells = 10
	filled := pct * cells / 100
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
	style := styleFaint
	switch {
	case pct >= 85:
		style = styleErr
	case pct >= 60:
		style = styleWarn
	}
	return style.Render(fmt.Sprintf("ctx %s %d%%", bar, pct))
}

func (m *model) footerView() string {
	if m.pending != nil && !m.denying {
		return m.permissionView()
	}
	var parts []string
	if m.denying {
		parts = append(parts, "  "+styleWarn.Bold(true).Render("denied "+m.pending.call.Name)+styleDim.Render(" · tell the model why (optional) · enter sends · esc skips"))
	}
	if c, idx := m.menu(); len(c) > 0 {
		start := 0
		if idx >= menuRows {
			start = idx - menuRows + 1
		}
		end := min(len(c), start+menuRows)
		for i := start; i < end; i++ {
			cmd := c[i]
			row := styleKey.Render(cmd.name) + " " + styleDim.Render(cmd.args) + "  " + styleFaint.Render(cmd.help)
			if i == idx {
				row = styleAccent.Render("▸ ") + styleKey.Bold(true).Render(cmd.name) + " " + styleDim.Render(cmd.args) + "  " + styleDim.Render(cmd.help)
			} else {
				row = "  " + row
			}
			parts = append(parts, truncateLine(row, m.width-1))
		}
		if len(c) > menuRows {
			parts = append(parts, styleFaint.Render(fmt.Sprintf("  ↑↓ choose · %d of %d · tab or enter accepts", idx+1, len(c))))
		}
	}
	for i, q := range m.queued {
		parts = append(parts, "  "+styleFaint.Render(fmt.Sprintf("queued %d ", i+1))+styleDim.Render(truncateLine(strings.ReplaceAll(q, "\n", " "), m.width-14)))
	}
	box := styleInputBox
	if !m.running {
		box = styleInputHot
	}
	prompt := styleAccent.Render("❯ ")
	if m.running {
		prompt = styleFaint.Render("❯ ")
	}
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
	sb.WriteString(styleWarn.Bold(true).Render("allow ") + styleTool.Bold(true).Render(c.Name) + " " + styleDim.Render(summarizeInput(c.Name, string(c.Input), m.width-len(c.Name)-16)))
	if c.Warning != "" {
		sb.WriteString("\n" + styleErr.Bold(true).Render("⚠ "+c.Warning))
	}
	sb.WriteString("\n" + styleKey.Render("y") + styleDim.Render(" allow once   ") + styleKey.Render("a") + styleDim.Render(" always for "+c.Name+"   ") + styleKey.Render("n") + styleDim.Render(" deny (and say why)   ") + styleFaint.Render("(diff shown above · ctrl+o expands)"))
	return stylePermBox.Width(m.width - 2).Render(sb.String())
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
