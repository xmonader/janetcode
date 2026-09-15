// Package tui is the interactive terminal interface built on bubbletea.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
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
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/theme"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/weburl"
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
	// Features is the catalogue of what janetcode can do; /help <topic>
	// searches it and the embedded manual. Nil leaves /help as it was.
	Features *feature.Catalog
	// Usage counts which catalogue features the user has reached for, so
	// hints can skip what they already know and /stats can name what they
	// have not tried. It stays on this machine; nil disables counting.
	Usage *feature.Usage
	// Hints is the one-time hint engine. Nil, or an engine with Off set,
	// means the session says nothing beyond what it is asked.
	Hints *hints.Engine
	// Untrusted names project files that are present but waiting for
	// approval (.mcp.json and friends), for the hint that says so.
	Untrusted []string
	// UnpricedModel is the configured model when nothing can price it.
	UnpricedModel string
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
	Typed *weburl.Set
	// AutoTitle asks the model for a short session title after the first turn.
	AutoTitle bool
	// Doctor writes the same checkup janetcode --doctor prints. Nil
	// leaves /doctor as a short local note.
	Doctor   func(io.Writer) error
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
	// Workspace is the shared host the file and shell tools use. /ssh
	// and /docker retarget it in place. Nil keeps tools on Cwd.
	Workspace *tool.Workspace
	Resumed   int // messages loaded from a resumed session
	Version   string
	// Theme is "auto"/"" (detect), "dark", "light", or the name of a
	// builtin or user theme.
	Theme string
	// Themes is the resolved, validated set of builtins plus user themes,
	// from config. Nil falls back to the builtins alone.
	Themes *theme.Set
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
	m.asker.send = p.Send
	opts.Agent.Approver = m.approver
	opts.Agent.Asker = m.asker
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
	Trust          trust.Store
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
	Trust          trust.Store
}

// PermOptions describes the permission rules in effect for /permissions.
type PermOptions struct {
	Project        perm.Rules
	ProjectHash    string
	ProjectTrusted bool
	Trust          trust.Store
	// Save persists a rule to the config file.
	Save func(kind string, r perm.Rule) error
}

type model struct {
	opts Options
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
	viewport   viewport.Model
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

	running    bool
	cancelling bool
	compacting bool
	cancel     context.CancelFunc
	startedAt  time.Time // when the running turn began
	// phaseUntil is when the thing the phase names is due, so a wait the
	// provider announced once counts down instead of standing still.
	phaseUntil time.Time
	// openedAt is when this window opened. /stats used startedAt, which
	// is the turn clock: before the first turn it was the zero time and
	// the session was reported as up for 2562047h47m16.854775807s.
	openedAt      time.Time
	phase         string // what the agent is doing right now
	pending       *permissionMsg
	denying       bool // the input box is collecting a reason for a denial
	status        string
	lastTurn      string                   // usage summary of the last turn
	lastVerdict   string                   // the last turn's verdict line
	ticks         int                      // tick count, for slow periodic checks
	lastUsage     llm.Usage                // the last turn's usage
	hints         *hints.Engine            // event-driven one-time hints; nil disables them
	hintState     hints.State              // what the hint rules are allowed to look at
	bangSeen      map[string]int           // !commands the user ran, by command line
	hintObs       hintObservations         // what the hint observers remember between events
	queued        []string                 // prompts typed while a turn was running
	steering      []string                 // notes handed to the running turn (ctrl+s)
	custom        map[string]prompt.Custom // user-defined slash commands
	clipboard     func() ([]byte, error)   // reads a PNG from the clipboard; swapped in tests
	copyText      func(string) error       // writes text to the clipboard; swapped in tests
	compIdx       int                      // highlighted row of the completion menu
	compArgs      bool                     // the menu is offering arguments, not commands
	compFor       string                   // input value the highlight belongs to
	lastFind      string                   // last /find query, for repeats
	lastFindAt    int                      // line of the last hit, so repeats advance past it
	picker        *picker                  // open searchable list
	connect       *connect                 // open provider/model dialog
	overlay       string                   // live panel over the transcript (overlayAgents); empty is closed
	overlayIdx    int                      // highlighted child in the /agents list
	overlayChild  string                   // id whose transcript is open; empty is the list
	overlayScroll int                      // line offset in the child transcript
	overlayFollow bool                     // keep the child transcript pinned to the tail
	overlayNote   string                   // kill/error line inside a jobs overlay
	planH         int                      // last reserved plan-strip height, so a rewrite relayouts
	connectGen    int
	quitting      bool
	branch        string
	// gutterOff is the hysteresis latch for the transcript margin: it
	// turns off below gutterCollapseWidth and stays off until the
	// terminal is at least gutterRestoreWidth, so resizing around 88
	// does not slam the margin on and off.
	gutterOff bool
	// notice is a one-shot line for the reserved row above the input
	// (paste-chip confirmation, outside branch change). It yields to a
	// live queue or steering notice and expires after noticeFor.
	notice   string
	noticeAt time.Time
	// term writes the escape sequences that let the terminal take part:
	// OSC 133 zones, desktop notifications, the window title.
	term *termIntegration
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

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleAccent))

	applyColorProfile(os.Getenv)
	// The terminal background is probed exactly once, here, before any
	// theme pins lipgloss to a mode: a live /theme switch to a single-mode
	// theme changes what lipgloss reports, so auto and adaptive themes must
	// remember the real answer rather than re-reading it (re-reading would
	// also talk to the terminal and eat keystrokes).
	bgDark := lipgloss.HasDarkBackground()
	th, dark := resolveTheme(opts.Theme, opts.Themes, bgDark)
	lipgloss.SetHasDarkBackground(dark)
	applyTheme(th)
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
	m.openedAt = time.Now()
	m.transcript.cwd = opts.Cwd
	m.clipboard = readClipboardImage
	m.copyText = writeClipboardText
	if opts.Session != nil {
		m.histFile = histPath(dirOf(opts.Session), opts.Cwd)
		m.history = loadPromptHistory(m.histFile)
		m.histIdx = len(m.history)
	}
	m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(opts.Cwd, opts.ConfigDir) {
		m.custom["/"+c.Name] = c
	}
	m.hints = opts.Hints
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
		if !m.hints.Given(firstRunID) && !m.hints.Silent() {
			if firstRun = m.firstRunLine(); firstRun != "" {
				m.transcript.tip(firstRun)
				m.hints.MarkShown(firstRunID)
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
		m.hints.MarkSpent("verify-gate")
	}
	m.hintState.Untrusted = opts.Untrusted
	m.hintState.UnpricedModel = opts.UnpricedModel
	m.hintState.ResumedAfterCompaction = opts.Resumed > 0 && resumedAfterCompaction(opts.Session)
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
		fmt.Fprintf(&sb, "janetcode %s · no provider configured yet: pick one below, or set ANTHROPIC_API_KEY / OPENAI_API_KEY\n", m.opts.Version)
	default:
		fmt.Fprintf(&sb, "janetcode %s · ", m.opts.Version)
	}
	fmt.Fprintf(&sb, "working in %s", fitPath(shortPath(m.opts.Cwd), 60))
	if m.branch != "" {
		fmt.Fprintf(&sb, " on %s", m.branch)
	}
	sb.WriteString("\n")
	switch modes := m.opts.Agent.Settings(); {
	case modes.AutoApprove:
		sb.WriteString("auto-approve is on: tools run without asking · esc cancels the turn")
	case modes.AutoMode:
		sb.WriteString("auto mode: in-workspace edits and known-safe reads run · other commands ask")
	case modes.AutoEdits:
		sb.WriteString("edits go through, commands ask (/edits) · esc denies")
	default:
		// esc at a prompt denies the one call; c stops the whole turn.
		sb.WriteString("edits and commands ask first · y allow · a always · n deny · esc denies · c cancels")
	}
	sb.WriteString(" · /help\n")
	sb.WriteString("@file attaches · !cmd runs and shares the output · /undo reverts a bad change")
	return sb.String()
}

// resumedLine names a reopened session for the banner.
func resumedLine(id string, messages int) string {
	return fmt.Sprintf("resumed session %s · %s", id, textfmt.Plural(messages, "message"))
}

func (m *model) Init() tea.Cmd {
	if ag := m.opts.Agent; ag != nil && ag.Cron != nil {
		send := m.send
		ag.Cron.OnFire = func(id, prompt string) {
			send(cronFireMsg{id: id, prompt: prompt})
		}
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
		// A word that spells a key name is text, not that key. It is
		// delivered one character at a time, which is what was typed, so
		// neither this model nor the textarea can mistake it. Not at a
		// permission prompt: there a run of characters is still ignored
		// as a whole rather than answering with its first letter.
		if typedAsKey(msg) && m.pending == nil {
			var cmds []tea.Cmd
			var mm tea.Model = m
			for _, r := range msg.Runes {
				var cmd tea.Cmd
				mm, cmd = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Paste: msg.Paste})
				cmds = append(cmds, cmd)
			}
			return mm, tea.Batch(cmds...)
		}
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
		expired := m.notice != "" && time.Since(m.noticeAt) >= noticeFor
		if expired {
			m.notice = ""
		}
		if m.ticks%10 == 0 && !m.running {
			// Someone may have checked out another branch in this tree
			// (or in the session's worktree) from outside; follow it.
			m.followBranch()
		}
		if m.running || m.overlay != "" {
			m.refresh(false)
		} else if notes := m.opts.Jobs.Finished(); len(notes) > 0 {
			for _, n := range notes {
				m.note(strings.SplitN(n, "\n", 2)[0])
				m.opts.Agent.AddHarnessContext("[background job update]\n" + n)
			}
			m.refresh(false)
		} else if expired {
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
		m.setPhase("waiting for your approval")
		m.term.announce("janetcode", "approval needed: "+msg.call.Name)
		m.refresh(false)
		return m, nil

	case askMsg:
		m.drainEvents()
		m.openAsk(msg)
		m.term.announce("janetcode", "a question for you")
		return m, nil

	case subagentDoneMsg:
		if msg.err != nil {
			m.transcript.errorf("subagent %s: %v", msg.desc, msg.err)
		} else {
			m.note("subagent finished · " + msg.desc + " · /agents to read it")
		}
		m.refresh(false)
		return m, nil

	case recipeDoneMsg:
		return m, m.finishRecipe(msg)

	case btwMsg:
		if msg.err != nil {
			m.transcript.errorf("btw: %v", msg.err)
		} else {
			m.transcript.info("btw " + msg.q + "\n" + msg.a)
		}
		m.refresh(false)
		return m, nil

	case doctorMsg:
		if msg.report != "" {
			m.transcript.info(msg.report)
		}
		if msg.err != nil {
			m.transcript.errorf("doctor: %v", msg.err)
		}
		m.refresh(false)
		return m, nil

	case undoAboutMsg:
		cmd := m.finishUndoAbout(msg)
		m.refresh(false)
		return m, cmd

	case evalKeepMsg:
		cmd := m.finishEvalKeep(msg)
		m.refresh(false)
		return m, cmd

	case undoApplyMsg:
		cmd := m.finishUndoApply(msg)
		m.refresh(false)
		return m, cmd

	case cronFireMsg:
		if m.running {
			m.note("loop " + msg.id + " skipped · a turn is running")
			m.refresh(false)
			return m, nil
		}
		m.note("loop " + msg.id)
		return m, m.submitAs(msg.prompt, "loop "+msg.id+" · "+msg.prompt)

	case verifyDoneMsg:
		m.finishVerify(msg)
		m.refresh(false)
		return m, nil

	case mcpPromptMsg:
		m.running = false
		m.setPhase("")
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
			m.note("mcp " + msg.name + ": authorized · restarting the server")
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
			m.note(line + " · /mcp")
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
			m.note("memory: extraction failed: " + msg.err.Error())
		}
		for _, e := range msg.added {
			m.note("remembered: " + memoryLine(e))
		}
		m.refresh(false)
		return m, nil
	}

	var cmd tea.Cmd
	// The footer height is reserved except for the completion menu; the
	// viewport has to be told when the menu opens or closes, or the view
	// is taller than the terminal and the transcript scrolls under it.
	before := m.footerHeight()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	if m.footerHeight() != before {
		m.layout()
		m.refresh(false)
	}
	// Typed letters must not move the transcript: the viewport's default
	// keymap treats h/j/k/l/b/f/u/d and space as pager keys, so typing
	// "/history …" used to scroll (and unpin) one line at a time.
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		cmds = append(cmds, m.scroll(msg))
	}
	return m, tea.Batch(cmds...)
}

func (m *model) drainEvents() {
	for _, ev := range m.queue.drain() {
		m.applyEvent(ev)
	}
}

func (m *model) applyEvent(ev agent.Event) {
	m.transcript.apply(ev)
	m.observeEvent(ev)
	switch ev.Kind {
	case agent.EvThinking:
		m.setPhase("thinking")
	case agent.EvText:
		m.setPhase("writing")
	case agent.EvToolStart:
		m.setPhase("preparing " + ev.Call.Name)
	case agent.EvToolInput:
		// Streamed input: the status says what is being written and how
		// much, instead of "preparing write_file" for a minute.
		if e := m.transcript.lastTool(ev.Call); e != nil && e.call.Name != "" {
			if s := streamingSummary(e.call.Name, e.input.String(), m.opts.Cwd); s != "" && s != "…" {
				m.setPhase(e.call.Name + " " + s)
			}
		}
	case agent.EvPhase:
		// The provider or the loop said what it is waiting on: a retry
		// countdown, a compaction, a subagent.
		m.setPhase(ev.Text)
	case agent.EvToolRun:
		m.setPhase("running " + ev.Call.Name)
		if ev.Call.Name == "task" {
			m.setPhase("waiting on subagent")
		}
	case agent.EvToolResult:
		m.setPhase("thinking")
		if ev.Call != nil && ev.Call.Name == "goal_status" && !ev.IsError {
			m.status = strings.TrimSpace(ev.Output)
		}
	case agent.EvCompacted:
		m.setPhase("thinking")
	case agent.EvVerdict:
		m.lastVerdict = shortenPaths(m.opts.Cwd, ev.Text)
	case agent.EvTurnDone:
		u := ev.Usage
		m.lastUsage = u
		in := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
		s := fmt.Sprintf("in %s · out %s", textfmt.Tokens(in), textfmt.Tokens(u.OutputTokens))
		if usd, ok := u.Cost(m.opts.Agent.Settings().Price); ok {
			s += " · " + llm.FormatUSD(usd)
			if u.CostSource(m.opts.Agent.Settings().Price) != "reported" {
				s += " est."
			}
		}
		// A ceiling the session will stop on belongs where the spend is,
		// not only in the message that announces it has been reached.
		if m.opts.Agent.MaxCost() > 0 {
			s += " · budget " + m.budgetStatus()
		}
		if ev.StopReason != "" && ev.StopReason != "end_turn" {
			s += " · stopped: " + string(ev.StopReason)
		}
		m.lastTurn = s
	}
}

func (m *model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.quitting {
		return ""
	}
	if m.picker != nil || m.connect != nil || m.overlay != "" || m.asking != nil {
		// A modal is the input: give it the body plus the reserved
		// footer so a short terminal still has room for the highlight.
		bodyH := m.height - 2 // header, status
		if bodyH < 3 {
			bodyH = 3
		}
		body := ""
		switch {
		case m.asking != nil:
			body = m.askView(bodyH)
		case m.picker != nil:
			body = m.pickerView(bodyH)
		case m.connect != nil:
			body = m.connectView(bodyH)
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
