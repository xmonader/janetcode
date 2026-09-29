// The model's Update half: the Bubble Tea message loop, the routing to
// whichever sub-model owns the key, and folding an agent event into the
// transcript. The model itself and its View are in tui.go.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Update is the loop. A window resize, a key and a spinner tick are
// handled here because they are the three that can fall through to the
// textarea and the viewport at the bottom of this function; everything
// else is a message from somewhere that finished, and is routed to one
// of the three handlers below.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.resize(msg)

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

	case spinner.TickMsg:
		if m.turn.running || (m.connect != nil && m.connect.loading) {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
			m.refresh(false)
		}

	default:
		for _, h := range []func(tea.Msg) (tea.Cmd, bool){m.updateTerminal, m.updateTurn, m.updateBackground} {
			if cmd, handled := h(msg); handled {
				return m, cmd
			}
		}
	}

	var cmd tea.Cmd
	// The footer height changes for two reasons: the completion menu
	// opening or closing, and the composer growing or shrinking with
	// what was just typed (see inputHeight, which is a pure function of
	// the textarea's content and width). before is read from the old
	// content, and the comparison from the new content once the key has
	// been applied to m.input below, so this only relayouts — and only
	// resizes the viewport — on a frame where the row count actually
	// changed, never on every keystroke.
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

// resize relays out for a new terminal size. The first one is also when
// the window exists at last, which is when a dialog or a prompt handed
// in on the command line can finally be put up.
func (m *model) resize(msg tea.WindowSizeMsg) tea.Cmd {
	first := !m.ready
	m.width, m.height = msg.Width, msg.Height
	m.layout()
	m.refresh(true)
	if first && m.opts.Connect {
		m.opts.Connect = false
		return m.openConnect(stepPick)
	}
	if first && m.opts.InitialPrompt != "" {
		text := m.opts.InitialPrompt
		m.opts.InitialPrompt = ""
		m.hist.remember(text)
		return m.submit(text)
	}
	return nil
}

// updateTerminal handles what the terminal and the clock say: focus,
// the mouse, and the half-second tick that keeps elapsed timers and
// throttled live markdown moving when no event arrives.
func (m *model) updateTerminal(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		m.term.focused = true
		return nil, true

	case tea.BlurMsg:
		m.term.focused = false
		return nil, true

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if m.clickTranscript(msg.Y) {
				return nil, true
			}
		}
		return m.scroll(msg), true

	case tickMsg:
		// Keeps elapsed timers and throttled live markdown moving even when
		// no event arrives.
		m.ticks++
		expired := m.notice.expire()
		if m.ticks%10 == 0 && !m.turn.running {
			// Someone may have checked out another branch in this tree
			// (or in the session's worktree) from outside; follow it.
			m.followBranch()
		}
		if m.turn.running || m.ov.kind != "" {
			m.refresh(false)
		} else if notes := m.opts.Jobs.Finished(); len(notes) > 0 {
			for _, n := range notes {
				m.note(strings.SplitN(n, "\n", 2)[0])
				m.opts.Agent.AddHarnessContext("[background job update]\n" + n)
			}
			m.refresh(false)
		} else if notes := m.opts.Agent.FinishedForks(); len(notes) > 0 {
			// A fork that ended while the session sat idle: the user sees
			// it now, and the model reads it when the next turn starts.
			for _, n := range notes {
				m.note(strings.SplitN(n, "\n", 2)[0])
				m.opts.Agent.AddHarnessContext("[background fork update]\n" + n)
			}
			m.refresh(false)
		} else if expired {
			m.refresh(false)
		}
		return tea.Batch(tick(), m.runStatusLine()), true

	case statusLineMsg:
		m.showStatusLine(msg)
		return nil, true

	case flashDoneMsg:
		// The flash has faded: one redraw puts the ordinary mark back on
		// every line that was bright, and if more calls finished while
		// this tick was in flight, one more timer covers all of them.
		m.flashPending = false
		m.refresh(false)
		return m.scheduleFlash(), true
	}
	return nil, false
}

// scheduleFlash arms the single timer that ends the finished-tool flash.
// A Bubble Tea command is the model's only clock, and a finished entry is
// rendered once and cached: without a tick the bright mark would be baked
// in and stay bright until something else happened to redraw that line.
//
// One timer serves every call that has finished, because they share one
// deadline (transcript.flashUntil) and the latch refuses a second. There
// is none at all when the flash cannot be seen: not under ansiOnly, where
// there is no flash, and not while the user has scrolled away from the
// tail, where redrawing the bottom of the transcript would cost a frame
// for something off screen.
func (m *model) scheduleFlash() tea.Cmd {
	if m.flashPending || m.styles == nil || m.styles.ansiOnly || !m.pinned {
		return nil
	}
	d := time.Until(m.transcript.flashUntil)
	if d <= 0 {
		return nil
	}
	m.flashPending = true
	return tea.Tick(d, func(time.Time) tea.Msg { return flashDoneMsg{} })
}

// updateTurn handles the messages the running turn produces: its
// events, the prompts it puts to the user, and its end.
func (m *model) updateTurn(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case flushMsg:
		m.drainEvents()
		m.refresh(false)
		return m.scheduleFlash(), true

	case permissionMsg:
		m.drainEvents() // the tool entry must exist before the prompt shows
		if m.pending != nil {
			// Another caller is being asked already — subagents run side
			// by side and each can bubble a prompt up. This one waits its
			// turn: putting it up in place of the first left the first
			// caller waiting for an answer that could no longer be given.
			m.waitingPerms = append(m.waitingPerms, msg)
			return nil, true
		}
		m.showPermission(msg)
		return nil, true

	case askMsg:
		m.drainEvents()
		m.openAsk(msg)
		m.term.announce("janetcode", "a question for you")
		return nil, true

	case agentDoneMsg:
		m.drainEvents()
		cmd := m.finishTurn(msg)
		if msg.err == nil && m.opts.Memory != nil && m.opts.AutoMemory && (m.opts.Agent.LastTurnMutated() || memory.LooksLikeFeedback(m.hist.lastUser)) {
			cmd = tea.Batch(cmd, m.extractMemory())
		}
		if msg.err == nil {
			if s := m.maybeSleep(); s != nil {
				cmd = tea.Batch(cmd, s)
			}
		}
		if msg.err == nil && m.opts.AutoTitle && m.opts.Session != nil && m.opts.Session.Meta.Title == "" && len(m.opts.Agent.Messages) >= 2 {
			cmd = tea.Batch(cmd, m.autoTitle())
		}
		m.refresh(false)
		return cmd, true

	case followupMsg:
		if msg.gen == m.followupGen && m.followupOn && !m.turn.running && strings.TrimSpace(m.input.Value()) == "" {
			m.followup = msg.text
			m.refresh(false)
		}
		return nil, true

	case titleMsg:
		if msg.gen == m.titleGen && m.opts.Session != nil && m.opts.Session.Meta.Title == "" && msg.title != "" {
			if err := m.opts.Session.SetTitle(msg.title); err != nil {
				m.transcript.errorf("could not save the title: %v", err)
			}
			m.refresh(false)
			return tea.SetWindowTitle(m.windowTitle()), true
		}
		return nil, true

	case cronFireMsg:
		if m.turn.running {
			m.note("loop " + msg.id + " skipped · a turn is running")
			m.refresh(false)
			return nil, true
		}
		m.note("loop " + msg.id)
		return m.submitAs(msg.prompt, "loop "+msg.id+" · "+msg.prompt), true

	case mcpPromptMsg:
		m.turn.running = false
		m.turn.setPhase("")
		if msg.err != nil {
			m.transcript.errorf("%s: %v", msg.command, msg.err)
			cmd := m.sendQueued()
			m.refresh(false)
			return cmd, true
		}
		return m.submit(msg.text), true

	case memoryMsg:
		if msg.err != nil {
			m.note("memory: " + msg.err.Error())
		}
		if msg.note != "" {
			m.note(msg.note)
		}
		for _, e := range msg.added {
			m.note("remembered: " + memoryLine(e))
		}
		m.refresh(false)
		return nil, true
	}
	return nil, false
}

// updateBackground handles work that was started and left to finish on
// its own: subagents, recipes, /verify, MCP, shell commands, and the
// commands that answer with a report.
func (m *model) updateBackground(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case modelsMsg:
		m.handleModels(msg)
		return nil, true

	case subagentDoneMsg:
		if msg.err != nil {
			m.transcript.errorf("subagent %s: %v", msg.desc, msg.err)
		} else {
			m.note("subagent finished · " + msg.desc + " · /agents to read it")
		}
		m.refresh(false)
		return nil, true

	case recipeDoneMsg:
		return m.finishRecipe(msg), true

	case btwMsg:
		if msg.err != nil {
			m.transcript.errorf("btw: %v", msg.err)
		} else {
			m.transcript.info("btw " + msg.q + "\n" + msg.a)
		}
		m.refresh(false)
		return nil, true

	case inspectMsg:
		if msg.err != nil {
			m.transcript.errorf("%v", msg.err)
		} else {
			m.transcript.info(msg.report)
		}
		m.refresh(false)
		return nil, true

	case doctorMsg:
		if msg.report != "" {
			m.transcript.info(msg.report)
		}
		if msg.err != nil {
			m.transcript.errorf("doctor: %v", msg.err)
		}
		m.refresh(false)
		return nil, true

	case undoAboutMsg:
		cmd := m.finishUndoAbout(msg)
		m.refresh(false)
		return cmd, true

	case evalKeepMsg:
		cmd := m.finishEvalKeep(msg)
		m.refresh(false)
		return cmd, true

	case undoApplyMsg:
		cmd := m.finishUndoApply(msg)
		m.refresh(false)
		return cmd, true

	case verifyDoneMsg:
		cancelled := m.turn.cancelling
		m.finishVerify(msg)
		if cancelled {
			// esc stopped the checks: what was queued behind them goes
			// back in the input, as it does when a turn is cancelled.
			m.turn.cancelling = false
			m.restoreQueued()
			m.refresh(false)
			return nil, true
		}
		cmd := m.sendQueued()
		m.refresh(false)
		return cmd, true

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
		return nil, true

	case mcpCmdMsg:
		if m.status != "" && strings.HasSuffix(m.status, "…") {
			m.status = ""
		}
		m.showResult(msg.res, msg.err)
		m.refresh(false)
		return nil, true

	case mcpProjectMsg:
		if o := m.opts.MCP; o != nil {
			// The user wrote this file themselves, so it is trusted.
			o.Project, o.ProjectHash, o.ProjectTrusted = msg.servers, msg.hash, true
		}
		return nil, true

	case mcpFetchMsg:
		return m.handleMCPFetch(msg), true

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
		return nil, true

	case voiceDoneMsg:
		m.finishVoice(msg)
		m.refresh(false)
		return nil, true
	case shellDoneMsg:
		m.finishShell(msg)
		var cmd tea.Cmd
		if next, ok := m.turn.dequeue(); ok {
			cmd = m.submit(next)
		}
		m.refresh(false)
		return cmd, true
	}
	return nil, false
}

func (m *model) drainEvents() {
	for _, ev := range m.queue.drain() {
		m.applyEvent(ev)
	}
}

func (m *model) applyEvent(ev agent.Event) {
	if !namesItsCall(ev) {
		return
	}
	m.turn.lastEventAt = time.Now()
	m.transcript.apply(ev)
	m.observeEvent(ev)
	switch ev.Kind {
	case agent.EvThinking:
		m.turn.setPhase(present.PhaseThinking)
	case agent.EvText:
		m.turn.setPhase(present.PhaseWriting)
	case agent.EvToolStart:
		m.turn.setPhase(present.PhasePreparing(ev.Call.Name))
	case agent.EvToolInput:
		// Streamed input: the status says what is being written and how
		// much, instead of "preparing write_file" for a minute.
		if e := m.transcript.lastTool(ev.Call); e != nil && e.call.Name != "" {
			if p, ok := present.PhaseStreaming(e.call.Name, e.input.String(), m.opts.Cwd); ok {
				m.turn.setPhase(p)
			}
		}
	case agent.EvPhase:
		// The provider or the loop said what it is waiting on: a retry
		// countdown, a compaction, a subagent.
		m.turn.setPhase(ev.Text)
	case agent.EvToolRun:
		m.turn.setPhase(present.PhaseRunning(ev.Call.Name, string(ev.Call.Input), m.opts.Cwd))
	case agent.EvToolResult:
		m.turn.setPhase(present.PhaseThinking)
		if ev.Call != nil && ev.Call.Name == "goal_status" && !ev.IsError {
			m.status = strings.TrimSpace(ev.Output)
		}
	case agent.EvCompacted:
		m.turn.setPhase(present.PhaseThinking)
	case agent.EvVerdict:
		m.lastVerdict = shortenPaths(m.opts.Cwd, ev.Text)
	case agent.EvTurnDone:
		u := ev.Usage
		m.lastUsage = u
		in := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
		// The cost leads and the token counts trail: a narrow status
		// line is cut from the right, and "$0…" kept the half nobody
		// reads while the gauge on the right already shows the tokens.
		var parts []string
		if usd, ok := u.Cost(m.opts.Agent.Settings().Price); ok {
			c := llm.FormatUSD(usd)
			if u.CostSource(m.opts.Agent.Settings().Price) != "reported" {
				c += " est."
			}
			parts = append(parts, c)
		}
		// A ceiling the session will stop on belongs where the spend is,
		// not only in the message that announces it has been reached.
		if m.opts.Agent.MaxCost() > 0 {
			parts = append(parts, "budget "+m.budgetStatus())
		}
		if ev.StopReason != "" && ev.StopReason != "end_turn" {
			parts = append(parts, "stopped: "+string(ev.StopReason))
		}
		m.lastTurn = strings.Join(parts, " · ")
		m.lastTurnTokens = fmt.Sprintf("in %s · out %s", textfmt.Tokens(in), textfmt.Tokens(u.OutputTokens))
	}
}
