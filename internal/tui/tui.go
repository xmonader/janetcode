// Package tui is the interactive terminal interface built on bubbletea.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

type model struct {
	opts Options
	// life ends when the window does. Every turn, background command and
	// server this session starts derives its context from it, so quitting
	// stops them rather than leaving them running against a program that
	// is no longer there.
	life    context.Context
	endLife context.CancelFunc
	// styles is what every view draws with; a theme switch replaces it.
	styles *styles
	// quitArmedAt is when ctrl+c was last pressed while idle.
	quitArmedAt time.Time
	// pendingClosedAt is when the last permission prompt closed.
	pendingClosedAt time.Time
	approver        *tuiApprover
	asker           *tuiAsker
	asking          *askState
	// send delivers messages from background goroutines into the loop.
	send func(tea.Msg)
	// titleGen invalidates generated titles once the user set one.
	titleGen int

	transcript *transcript
	queue      *eventQueue
	viewport   scroller
	input      textarea.Model
	spinner    spinner.Model

	width, height int
	ready         bool
	verbose       bool
	// bgDark is the terminal background probed once at startup. auto and
	// adaptive themes follow it; /theme switching never re-probes.
	bgDark bool
	// pinned is true while the transcript follows new output. The user
	// scrolling away from the bottom clears it; reaching the bottom again,
	// sending a prompt or starting a new session sets it. Resizing the
	// window never changes it, so a resize mid-stream does not yank a
	// reader who had scrolled up back to the tail.
	pinned      bool
	lastContent string // last transcript pushed into the viewport
	// flashPending is the one timer that ends the finished-tool flash.
	// It is a latch, not a count: a hundred calls returning in the same
	// second schedule one tick between them.
	flashPending bool

	// pastes holds the text behind each "[paste #N +M lines]" chip in the
	// input, in the order they were pasted.
	pastes []string
	// hist is the up/down recall of submitted prompts, loaded from and
	// appended to a file per working directory (see history.go).
	hist historyState

	// turn is the running turn: whether there is one, how to stop it,
	// what it is doing, and what is waiting behind it.
	turn turnState
	// openedAt is when this window opened. /stats used startedAt, which
	// is the turn clock: before the first turn it was the zero time and
	// the session was reported as up for 2562047h47m16.854775807s.
	openedAt       time.Time
	pending        *permissionMsg
	rung           int  // the highlighted rung of the pending call's rule ladder
	denying        bool // the input box is collecting a reason for a denial
	status         string
	lastTurn       string                   // cost summary of the last turn
	lastTurnTokens string                   // its token counts, said last
	lastVerdict    string                   // the last turn's verdict line
	ticks          int                      // tick count, for slow periodic checks
	lastUsage      llm.Usage                // the last turn's usage
	hint           hintsState               // the one-time hint engine and what it watches
	custom         map[string]prompt.Custom // user-defined slash commands
	clipboard      func() ([]byte, error)   // reads a PNG from the clipboard; swapped in tests
	copyText       func(string) error       // writes text to the clipboard; swapped in tests
	comp           completionState          // highlight in the completion menu
	skills         skillRowCache            // the menu's skill rows, as last read from disk
	goal           goalCache                // the status line's goal note, as last folded from the log
	finder         findState                // the last /find, so a repeat advances
	picker         *picker                  // open searchable list
	connect        *connect                 // open provider/model dialog
	wiz            *wizard                  // open step dialog (/mcp add)
	ov             overlayState             // live panel over the transcript; empty kind is closed
	traj           trajBrowse               // the trajectory overlay's own state: mode, filter, ordering
	planH          int                      // last reserved plan-strip height, so a rewrite relayouts
	connectGen     int
	quitting       bool
	branch         string
	// sline is the user's status line command's output (see statusline.go).
	sline statusLine
	// gutterOff is the hysteresis latch for the transcript margin: it
	// turns off below gutterCollapseWidth and stays off until the
	// terminal is at least gutterRestoreWidth, so resizing around 88
	// does not slam the margin on and off.
	gutterOff bool
	// notice is the one-shot line in the reserved row above the input.
	notice noticeState
	// term writes the escape sequences that let the terminal take part:
	// OSC 133 zones, desktop notifications, the window title.
	term *termIntegration
	// followupOn is the session toggle for ghost-text next-prompt
	// suggestions; followup is the last suggestion, followupGen
	// invalidates a stale one.
	followupOn  bool
	followup    string
	followupGen int
	// sleptGoals are the goal ids the memory consolidation has already
	// read this session's record for, so it runs once per finished goal.
	sleptGoals map[string]bool
}

const (
	minInputLines = 1
	maxInputLines = 6

	defaultPlaceholder = "Ask for a change, a fix, or an explanation… · / for commands"

	// noticeFor is how long a one-shot footer notice stays visible.
	noticeFor = 3 * time.Second
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

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(defaultStyles().accent))
	if opts.Plain {
		sp.Spinner = plainSpinner
	}

	_, ansi := applyColorProfile(os.Getenv)
	// The terminal background is probed exactly once, here, before any
	// theme pins lipgloss to a mode: a live /theme switch to a single-mode
	// theme changes what lipgloss reports, so auto and adaptive themes must
	// remember the real answer rather than re-reading it (re-reading would
	// also talk to the terminal and eat keystrokes).
	bgDark := lipgloss.HasDarkBackground()
	th, dark := resolveTheme(opts.Theme, opts.Themes, bgDark)
	lipgloss.SetHasDarkBackground(dark)
	st := newStyles(th, ansi)
	styleInput(&ta, st)
	style := "light"
	if dark {
		style = "dark"
	}
	m := &model{
		opts:       opts,
		bgDark:     bgDark,
		pinned:     true,
		approver:   &tuiApprover{},
		asker:      &tuiAsker{},
		styles:     st,
		transcript: newTranscriptStyled(style, st),
		queue:      &eventQueue{},
		input:      ta,
		spinner:    sp,
		term:       newTermIntegration(),
	}
	if opts.Config != nil {
		m.followupOn = opts.Config.FollowupEnabled()
	}
	m.life, m.endLife = context.WithCancel(context.Background())
	if b, ok := prompt.GitBranch(opts.Cwd); ok {
		m.branch = b
		if b == "" {
			m.branch = "git"
		}
	}
	m.openedAt = time.Now()
	m.transcript.cwd = opts.Cwd
	m.clipboard = readClipboardImage
	m.copyText = writeClipboardText
	if opts.Session != nil {
		m.hist.load(dirOf(opts.Session), opts.Cwd)
	}
	m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(opts.Cwd, opts.ConfigDir) {
		m.custom["/"+c.Name] = c
	}
	m.hint.engine = opts.Hints
	var firstRun string
	if opts.Resumed > 0 {
		m.transcript.banner(m.welcome(resumedLine(opts.Session.Meta.ID, opts.Resumed)))
		m.transcript.loadHistory(opts.Agent.Messages)
	} else {
		m.transcript.banner(m.welcome(""))
		// The first session in a directory janetcode has not seen gets
		// one specific line rather than a generic tip. The first session
		// only: it is recorded in the same per-project file the hints
		// use, because a line that says "this is a Go project" every
		// morning for a year is not orientation, it is noise, and a
		// project with no .janetcode directory of its own never grew one
		// to notice.
		if !m.hint.engine.Given(firstRunID) && !m.hint.engine.Silent() {
			if firstRun = m.firstRunLine(); firstRun != "" {
				m.transcript.tip(firstRun)
				m.hint.engine.MarkShown(firstRunID)
			}
		}
	}
	// The hints that are about the session's starting conditions — an
	// untrusted .mcp.json, a model nothing can price, a resumed session
	// whose history was already summarised — are evaluated here, once,
	// while the facts are in front of the user.
	if firstRun != "" && strings.Contains(firstRun, "/verify set") {
		// Already proposed, above; the rule that would propose it again
		// three turns from now is spent here.
		m.hint.engine.MarkSpent("verify-gate")
	}
	m.hint.state.Untrusted = opts.Untrusted
	m.hint.state.UnpricedModel = opts.UnpricedModel
	m.hint.state.ResumedAfterCompaction = opts.Resumed > 0 && resumedAfterCompaction(opts.Session)
	m.noteHint(nil)
	return m
}

// resumedAfterCompaction reports whether the session being reopened had
// already been summarised, which is why the model has a summary rather
// than the conversation.
func resumedAfterCompaction(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	for _, e := range sess.Events() {
		if e.Type == session.TypeCompactSummary {
			return true
		}
	}
	return false
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
	case m.opts.Agent.Settings().Provider == nil:
		fmt.Fprintf(&sb, "janetcode %s · not connected yet · /connect picks a provider, or set ANTHROPIC_API_KEY or OPENAI_API_KEY and restart\n", m.opts.Version)
	default:
		fmt.Fprintf(&sb, "janetcode %s · ", m.opts.Version)
	}
	fmt.Fprintf(&sb, "working in %s", fitPath(shortPath(m.opts.Cwd), 60))
	if m.branch != "" {
		fmt.Fprintf(&sb, " on %s", m.branch)
	}
	sb.WriteString("\n")
	// One line of what to know first: what runs without asking, and the
	// three things a new user would not guess. The keys at a permission
	// prompt are on the prompt itself when it comes.
	switch modes := m.opts.Agent.Settings(); {
	case modes.AutoApprove:
		sb.WriteString("yolo: tools run without asking · esc cancels")
	case modes.AutoMode:
		sb.WriteString("auto: workspace edits and safe reads run, the rest asks")
	case modes.AutoEdits:
		sb.WriteString("edits go through, commands ask")
	default:
		sb.WriteString("edits and commands ask first")
	}
	sb.WriteString(" · @file attaches · !cmd runs · /undo reverts · /help")
	return sb.String()
}

// resumedLine names a reopened session for the banner.
func resumedLine(id string, messages int) string {
	return fmt.Sprintf("resumed session %s · %s", id, textfmt.Plural(messages, "message"))
}

func (m *model) Init() tea.Cmd {
	if ag := m.opts.Agent; ag != nil && ag.Cron != nil {
		send := m.send
		// SetOnFire, not a field write: a resumed session arms its loops
		// before Init runs, so a one-second loop can be reading OnFire
		// from its timer goroutine while this assigns it.
		ag.Cron.SetOnFire(func(id, prompt string) {
			send(cronFireMsg{id: id, prompt: prompt})
		})
	}
	for _, n := range m.opts.Notes {
		m.transcript.info(n)
	}
	m.startMCP()
	return tea.Batch(textarea.Blink, m.spinner.Tick, tick(), tea.SetWindowTitle(m.windowTitle()))
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) View() string {
	if m.opts.Plain {
		return toPlain(m.view())
	}
	return m.view()
}

func (m *model) view() string {
	if !m.ready {
		return "loading…"
	}
	if m.quitting {
		return ""
	}
	if mode := m.mode(); mode.modal() {
		// A modal is the input: give it the body plus the reserved
		// footer so a short terminal still has room for the highlight.
		// Which one is drawn is mode()'s answer, not a second switch of
		// its own: the two used to rank overlay and picker differently,
		// so with both up the picker was on screen and the overlay was
		// taking the keys.
		bodyH := m.height - 2 // header, status
		if bodyH < 3 {
			bodyH = 3
		}
		body := ""
		switch mode {
		case modeAsk:
			body = m.askView(bodyH)
		case modePicker:
			body = m.pickerView(bodyH)
		case modeConnect:
			body = m.connectView(bodyH)
		case modeWizard:
			body = m.wizardView(bodyH)
		default:
			body = m.overlayView(bodyH)
		}
		return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), body, m.statusView())
	}
	parts := []string{m.headerView()}
	if p := m.planView(); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, m.viewport.View(), m.statusView(), m.footerView())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}
