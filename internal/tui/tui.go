// Package tui is the interactive terminal interface built on bubbletea.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// Options configures the UI.
type Options struct {
	Agent     *agent.Agent
	Session   *session.Session
	Provider  string
	Cwd       string
	Resumed   int // messages loaded from a resumed session
	Version   string
	// Theme is "dark", "light" or "" for terminal detection.
	Theme string
}

// Run starts the interactive UI and blocks until the user quits.
func Run(opts Options) error {
	m := newModel(opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.send = p.Send
	m.approver.send = p.Send
	opts.Agent.Approver = m.approver
	_, err := p.Run()
	return err
}

// --- messages from the agent goroutine ---

type agentEventMsg struct{ ev agent.Event }
type agentDoneMsg struct {
	err     error
	syncErr error
}
type sessionSwapMsg struct{ sess *session.Session }
type permissionMsg struct {
	call  agent.ToolCall
	reply chan agent.Decision
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

// --- model ---

type model struct {
	opts     Options
	approver *tuiApprover
	// send delivers messages from background goroutines into the loop.
	send func(tea.Msg)

	transcript *transcript
	viewport   viewport.Model
	input      textarea.Model
	spinner    spinner.Model

	width, height int
	ready         bool

	running   bool
	cancel    context.CancelFunc
	startedAt time.Time
	pending   *permissionMsg
	status    string
	quitting  bool
}

func newModel(opts Options) *model {
	ta := textarea.New()
	ta.Placeholder = "Ask janetcode anything. Enter to send, ctrl+j for a newline, /help for commands."
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
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
		input:      ta,
		spinner:    sp,
	}
	m.transcript.info(fmt.Sprintf("janetcode %s · %s/%s · %s", opts.Version, opts.Provider, opts.Agent.Model, opts.Cwd))
	if opts.Resumed > 0 {
		m.transcript.info(fmt.Sprintf("resumed session %s (%d messages)", opts.Session.Meta.ID, opts.Resumed))
		m.transcript.loadHistory(opts.Agent.Messages)
	} else {
		m.transcript.info("session " + opts.Session.Meta.ID + " · type /help for keys and commands")
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refresh(true)
		return m, nil

	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case spinner.TickMsg:
		if m.running {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case agentEventMsg:
		m.transcript.apply(msg.ev)
		if msg.ev.Kind == agent.EvTurnDone {
			m.status = usageLine(msg.ev)
		}
		m.refresh(false)
		return m, nil

	case sessionSwapMsg:
		m.opts.Session = msg.sess
		m.transcript.info("continuing in session " + msg.sess.Meta.ID)
		return m, nil

	case permissionMsg:
		m.pending = &msg
		m.refresh(false)
		return m, nil

	case agentDoneMsg:
		m.finishTurn(msg)
		m.refresh(false)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
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
		}
		return nil, true
	}
	switch msg.String() {
	case "ctrl+c":
		if m.running {
			m.cancelTurn()
			return nil, true
		}
		m.quitting = true
		return tea.Quit, true
	case "ctrl+d":
		m.quitting = true
		return tea.Quit, true
	case "esc":
		if m.running {
			m.cancelTurn()
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
		if m.running {
			m.status = "still working; press esc to cancel"
			return nil, true
		}
		m.input.Reset()
		if strings.HasPrefix(text, "/") {
			return m.command(text), true
		}
		return m.submit(text), true
	}
	return nil, false
}

func (m *model) answer(d agent.Decision) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- d
	m.transcript.permissionAnswered(m.pending.call, d)
	m.pending = nil
	m.refresh(false)
}

func (m *model) submit(text string) tea.Cmd {
	m.transcript.user(text)
	m.running = true
	m.startedAt = time.Now()
	m.status = ""
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, sess, send := m.opts.Agent, m.opts.Session, m.send
	m.refresh(false)
	go func() {
		err := ag.Run(ctx, text, func(e agent.Event) { send(agentEventMsg{ev: e}) })
		syncErr := sess.Sync(ag.Messages)
		send(agentDoneMsg{err: err, syncErr: syncErr})
	}()
	return m.spinner.Tick
}

// compact summarises the history in the background like a turn.
func (m *model) compact() tea.Cmd {
	m.running = true
	m.startedAt = time.Now()
	m.status = "compacting context..."
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ag, sess, send := m.opts.Agent, m.opts.Session, m.send
	m.transcript.info(fmt.Sprintf("compacting ~%d tokens of context...", ag.EstimateTokens()))
	m.refresh(false)
	go func() {
		err := ag.Compact(ctx, func(e agent.Event) { send(agentEventMsg{ev: e}) })
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		var syncErr error
		if err == nil {
			// Compaction rewrote the history: start a fresh session file so
			// the old transcript is kept intact on disk.
			if ns, nerr := session.New(dirOf(sess), m.opts.Cwd, m.opts.Provider, ag.Model); nerr == nil {
				syncErr = ns.Sync(ag.Messages)
				send(sessionSwapMsg{sess: ns})
			} else {
				syncErr = nerr
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
	m.status = "cancelling..."
}

func (m *model) finishTurn(msg agentDoneMsg) {
	m.running = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.pending = nil
	elapsed := time.Since(m.startedAt).Round(100 * time.Millisecond)
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.transcript.errorf("cancelled after %s", elapsed)
		m.status = "cancelled"
	case errors.Is(msg.err, agent.ErrMaxIterations):
		m.transcript.errorf("stopped: too many tool iterations in one turn (%d). Send a message to continue.", m.opts.Agent.MaxIterations)
	case msg.err != nil:
		m.transcript.errorf("%v", msg.err)
		m.status = "error"
	default:
		m.transcript.finishTurn()
		if m.status == "" {
			m.status = fmt.Sprintf("done in %s", elapsed)
		} else {
			m.status += fmt.Sprintf(" · %s", elapsed)
		}
	}
	if msg.syncErr != nil {
		m.transcript.errorf("could not save session: %v", msg.syncErr)
	}
}

func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/help", "/?":
		m.transcript.info(helpText)
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
		m.transcript.info("new session " + sess.Meta.ID)
	case "/yolo":
		m.opts.Agent.AutoApprove = !m.opts.Agent.AutoApprove
		if m.opts.Agent.AutoApprove {
			m.transcript.info("auto-approve ON: tools run without asking")
		} else {
			m.transcript.info("auto-approve OFF")
		}
	case "/model":
		if len(fields) < 2 {
			m.transcript.info("model: " + m.opts.Agent.Model)
			break
		}
		m.opts.Agent.Model = fields[1]
		m.transcript.info("model set to " + fields[1] + " (same provider)")
	case "/compact":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before compacting")
			break
		}
		return m.compact()
	case "/usage":
		u := m.opts.Agent.Usage
		m.transcript.info(fmt.Sprintf("session tokens: in %d · out %d · cache read %d · cache write %d", u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens))
	default:
		m.transcript.errorf("unknown command %s (try /help)", fields[0])
	}
	m.refresh(false)
	return nil
}

func dirOf(s *session.Session) string {
	p := s.Path()
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

const helpText = `Keys
  enter        send · ctrl+j / alt+enter  newline
  esc          cancel the running turn
  pgup/pgdn    scroll transcript (mouse wheel works too)
  ctrl+c       cancel, or quit when idle · ctrl+d quit
Permission prompt: y allow once · a allow this tool for the session · n deny
Commands
  /help /quit /clear (new session) /yolo (toggle auto-approve) /model <name>
  /usage (tokens) /compact (summarise context to free the window)`

// --- layout & view ---

const inputHeight = 3

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	vh := m.height - inputHeight - 3 // header, status, input border
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
	m.input.SetWidth(m.width - 2)
}

// refresh re-renders the transcript into the viewport. When the viewport was
// at the bottom it stays pinned there so streaming output scrolls.
func (m *model) refresh(resized bool) {
	if !m.ready {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.transcript.render(m.width, resized, m.pending))
	if atBottom || resized {
		m.viewport.GotoBottom()
	}
}

func (m *model) View() string {
	if !m.ready {
		return "loading..."
	}
	if m.quitting {
		return ""
	}
	header := styleHeader.Width(m.width).Render(fmt.Sprintf(" janetcode · %s/%s · %s", m.opts.Provider, m.opts.Agent.Model, m.opts.Session.Meta.ID))
	var status string
	switch {
	case m.pending != nil:
		status = styleWarn.Render(" permission needed: y allow · a always · n deny")
	case m.running:
		status = " " + m.spinner.View() + " " + styleDim.Render(fmt.Sprintf("working %s · esc to cancel", time.Since(m.startedAt).Round(time.Second)))
	default:
		status = " " + styleDim.Render(m.status)
	}
	auto := ""
	if m.opts.Agent.AutoApprove {
		auto = styleWarn.Render(" [yolo]")
	}
	statusLine := lipgloss.NewStyle().Width(m.width).Render(status + auto)
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		statusLine,
		styleInputBorder.Width(m.width-2).Render(m.input.View()),
	)
}

func usageLine(e agent.Event) string {
	u := e.Usage
	s := fmt.Sprintf("in %d · out %d", u.InputTokens, u.OutputTokens)
	if u.CacheReadTokens > 0 {
		s += fmt.Sprintf(" · cached %d", u.CacheReadTokens)
	}
	if e.StopReason != "" && e.StopReason != "end_turn" {
		s += " · stop: " + string(e.StopReason)
	}
	return s
}

var (
	styleAccent      = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleErr         = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleHeader      = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Bold(true)
	styleUser        = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	styleUserBody    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleTool        = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	styleInputBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(lipgloss.Color("240"))
)
