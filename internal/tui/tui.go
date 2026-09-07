// Package tui is the interactive terminal interface built on bubbletea.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
)

// Options configures the UI.
type Options struct {
	Agent    *agent.Agent
	Session  *session.Session
	Provider string
	Cwd      string
	Resumed  int // messages loaded from a resumed session
	Version  string
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
	reply chan agent.Decision
}
type tickMsg time.Time

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
	reply := make(chan agent.Decision, 1)
	a.send(permissionMsg{call: call, reply: reply})
	select {
	case d := <-reply:
		return d, nil
	case <-ctx.Done():
		return agent.Deny, ctx.Err()
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
	{"/yolo", "", "toggle auto-approve for tools"},
	{"/model", "<name>", "switch model on the same provider"},
	{"/usage", "", "token usage for this session"},
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
	status     string
	lastTurn   string // usage summary of the last turn
	queued     []string // prompts typed while a turn was running
	quitting   bool
	branch     string
}

const (
	minInputLines = 1
	maxInputLines = 6
)

func newModel(opts Options) *model {
	ta := textarea.New()
	ta.Placeholder = "Ask for a change, a fix, or an explanation…  (/ for commands)"
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
	m.transcript.banner(m.welcome())
	if opts.Resumed > 0 {
		m.transcript.info(fmt.Sprintf("resumed session %s · %d messages", opts.Session.Meta.ID, opts.Resumed))
		m.transcript.loadHistory(opts.Agent.Messages)
	}
	return m
}

func (m *model) welcome() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "janetcode %s · %s · %s\n", m.opts.Version, m.opts.Provider, m.opts.Agent.Model)
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

func (m *model) Init() tea.Cmd {
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
		if m.running {
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
		if ev.StopReason != "" && ev.StopReason != "end_turn" {
			s += " · stopped: " + string(ev.StopReason)
		}
		m.lastTurn = s
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// Permission prompt takes over the keyboard.
	if m.pending != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			m.answer(agent.Allow)
		case "a", "A":
			m.answer(agent.AllowSession)
		case "n", "N", "esc":
			m.answer(agent.Deny)
		case "ctrl+c":
			m.answer(agent.Deny)
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
		if m.running && !m.cancelling {
			m.cancelTurn()
			return nil, true
		}
		// Idle, or pressed twice while a cancel is still pending: quit.
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
	case "tab":
		if m.complete() {
			m.layout()
			m.refresh(false)
			return nil, true
		}
	case "up":
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
		return m.submit(text), true
	}
	return nil, false
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
	v := m.input.Value()
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " \n") {
		var out []command
		for _, c := range commands {
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

// mentionAtEnd returns the partial path of an @mention that ends the input.
func mentionAtEnd(v string) (string, bool) {
	i := strings.LastIndexAny(v, " \n\t")
	word := v[i+1:]
	if !strings.HasPrefix(word, "@") {
		return "", false
	}
	return word[1:], true
}

// complete applies the first completion to the input.
func (m *model) complete() bool {
	c := m.completions()
	if len(c) == 0 {
		return false
	}
	v := m.input.Value()
	if strings.HasPrefix(v, "/") {
		m.input.SetValue(c[0].name + " ")
	} else {
		i := strings.LastIndexAny(v, " \n\t")
		// Several matches: extend to their common prefix so tab narrows.
		name := c[0].name
		for _, o := range c[1:] {
			name = commonPrefix(name, o.name)
		}
		if len(c) == 1 && !strings.HasSuffix(name, "/") {
			name += " "
		}
		m.input.SetValue(v[:i+1] + name)
	}
	m.input.CursorEnd()
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

func (m *model) answer(d agent.Decision) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- d
	m.transcript.permissionAnswered(m.pending.call, d)
	m.pending = nil
	m.phase = "thinking"
	m.refresh(false)
}

func (m *model) submit(text string) tea.Cmd {
	m.transcript.user(text)
	text, atts := prompt.ExpandMentions(m.opts.Cwd, text)
	if len(atts) > 0 {
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
			if !a.Dir {
				names[i] += " (" + shortBytes(a.Size) + ")"
			}
		}
		m.transcript.info("attached " + strings.Join(names, " · "))
	}
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, sess, send, queue := m.opts.Agent, m.opts.Session, m.send, m.queue
	m.refresh(false)
	m.viewport.GotoBottom()
	go func() {
		err := ag.Run(ctx, text, queue.push)
		ns, syncErr := sess.SyncOrRotate(ag.Messages)
		if ns != sess {
			send(sessionSwapMsg{sess: ns})
		}
		send(agentDoneMsg{err: err, syncErr: syncErr})
	}()
	return m.spinner.Tick
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
		hint := "check ANTHROPIC_BASE_URL and your network"
		if provider == "openai" {
			hint = "check OPENAI_BASE_URL and your network"
		}
		return "cannot reach the API: " + s + "\n  " + hint
	case strings.Contains(l, "http 401") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		return "authentication failed: " + s + "\n  check the API key for provider " + provider
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		return "rate limited or out of quota: " + s + "\n  wait and resend, or switch model with /model"
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"):
		return "model not available: " + s + "\n  pick another with /model <name>"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "the request timed out: " + s
	}
	return s
}

func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/help", "/?":
		m.transcript.info(helpText())
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
	case "/model":
		if len(fields) < 2 {
			m.transcript.info("model: " + m.opts.Agent.Model + " (" + m.opts.Provider + ")")
			break
		}
		m.opts.Agent.Model = fields[1]
		m.transcript.info("model set to " + fields[1] + " on " + m.opts.Provider)
	case "/compact":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before compacting")
			break
		}
		return m.compact()
	case "/usage":
		u := m.opts.Agent.Usage
		m.transcript.info(fmt.Sprintf("session tokens · in %s · out %s · cache read %s · cache write %s · context now ~%s",
			shortTokens(u.InputTokens), shortTokens(u.OutputTokens), shortTokens(u.CacheReadTokens), shortTokens(u.CacheWriteTokens), shortTokens(m.opts.Agent.EstimateTokens())))
	default:
		m.transcript.errorf("unknown command %s (try /help)", fields[0])
	}
	m.refresh(false)
	return nil
}

func dirOf(s *session.Session) string {
	return filepath.Dir(s.Path())
}

func helpText() string {
	var sb strings.Builder
	sb.WriteString("keys\n")
	sb.WriteString("  enter send · ctrl+j / alt+enter newline · up/down recall prompts · tab complete /command\n")
	sb.WriteString("  esc cancel the turn (or clear the input) · ctrl+c cancel, twice to force quit · ctrl+d quit\n")
	sb.WriteString("  pgup/pgdn or mouse wheel scroll · ctrl+o expand tool output · ctrl+l redraw\n")
	sb.WriteString("  typing while the model works queues the message; it is sent when the turn ends\n")
	sb.WriteString("  @path attaches a file or directory listing to the prompt · tab completes the path\n")
	sb.WriteString("permission prompt: y allow once · a allow this tool for the session · n deny\n")
	sb.WriteString("commands\n")
	for _, c := range commands {
		fmt.Fprintf(&sb, "  %-10s %-7s %s\n", c.name, c.args, c.help)
	}
	return strings.TrimRight(sb.String(), "\n")
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
	if m.pending != nil {
		return m.permissionBoxHeight()
	}
	h := m.inputHeight() + 2 // border
	if c := m.completions(); len(c) > 0 {
		h += min(len(c), 6)
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
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.viewport.View(),
		m.statusView(),
		m.footerView(),
	)
}

func (m *model) headerView() string {
	left := styleHeaderHi.Render(" ◆ janetcode ") + styleHeader.Render(m.opts.Provider+"/"+m.opts.Agent.Model)
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
	if m.pending != nil {
		return m.permissionView()
	}
	var parts []string
	if c := m.completions(); len(c) > 0 {
		if len(c) > 6 {
			c = c[:6]
		}
		for _, cmd := range c {
			parts = append(parts, "  "+styleKey.Render(cmd.name)+" "+styleDim.Render(cmd.args)+"  "+styleFaint.Render(cmd.help))
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
	sb.WriteString("\n" + styleKey.Render("y") + styleDim.Render(" allow once   ") + styleKey.Render("a") + styleDim.Render(" always for "+c.Name+"   ") + styleKey.Render("n") + styleDim.Render(" deny   ") + styleFaint.Render("(diff shown above · ctrl+o expands)"))
	return stylePermBox.Width(m.width - 2).Render(sb.String())
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
