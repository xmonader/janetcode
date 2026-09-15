package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// cycleMode is shift+tab: ask → plan → auto → yolo → ask. /edits and
// /readonly stay explicit commands so they are not stolen by the cycle.
func (m *model) cycleMode() {
	ag := m.opts.Agent
	if ag == nil {
		return
	}
	switch cur := ag.Settings(); {
	case cur.PlanMode:
		ag.SetPlanMode(false)
		ag.UpdateSettings(func(s *agent.Settings) { s.ReadOnly, s.AutoApprove, s.AutoMode = false, false, true })
	case cur.AutoMode:
		ag.UpdateSettings(func(s *agent.Settings) { s.AutoMode, s.AutoApprove = false, true })
	case cur.AutoApprove:
		ag.UpdateSettings(func(s *agent.Settings) { s.AutoApprove, s.AutoMode = false, false })
	default:
		ag.UpdateSettings(func(s *agent.Settings) { s.AutoApprove, s.AutoMode = false, false })
		ag.SetPlanMode(true)
	}
	m.noteMode()
	m.refresh(false)
}

// typedKeyNames are the key names bubbletea gives a key press that a
// person can also type as a word. bubbles' key.Matches compares only
// KeyMsg.String(), so a run of typed characters that happens to spell
// one of these is indistinguishable from the key itself.
var typedKeyNames = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "insert": true, "delete": true,
	"pgup": true, "pgdown": true, "tab": true, "enter": true,
	"esc": true, "backspace": true, "space": true, "runes": true,
	"f1": true, "f2": true, "f3": true, "f4": true, "f5": true,
	"f6": true, "f7": true, "f8": true, "f9": true, "f10": true,
	"f11": true, "f12": true,
}

// typedAsKey reports whether a run of typed characters would be taken
// for a key press by anything that switches on KeyMsg.String().
func typedAsKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) > 1 &&
		typedKeyNames[string(msg.Runes)]
}

// keyName is the name to switch on for one key press. Bubbletea splits
// typed text into runs of runes between spaces and delivers each run as
// one KeyRunes message whose String() is the text itself, so the words
// "right", "left", "up", "down", "home", "end", "enter", "esc" and
// "delete" used to arrive as those keys: the word disappeared from the
// prompt, the cursor jumped, or ↑ pulled in a history entry in the
// middle of a sentence. Typed text of more than one rune is never a key
// name, so it falls through to the textarea.
func keyName(msg tea.KeyMsg) string {
	if msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) > 1 {
		return ""
	}
	return msg.String()
}

// handleKey routes one key press. The modals and the permission prompt
// each take the whole keyboard while they are up; outside them the key
// table below says what a key does, and a key with no entry falls
// through to the textarea.
func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case m.asking != nil:
		return m.handleAskKey(msg)
	case m.ov.kind != "":
		return m.handleOverlayKey(msg)
	case m.picker != nil:
		return m.handlePickerKey(msg)
	case m.connect != nil:
		return m.handleConnectKey(msg)
	}
	if m.pending == nil && msg.Paste && bigPaste(string(msg.Runes)) {
		return m.chipPaste(string(msg.Runes))
	}
	// Permission prompt takes over the keyboard.
	if m.pending != nil {
		if m.denying {
			return m.handleDenyKey(msg)
		}
		return m.handlePendingKey(msg)
	}
	if act, ok := promptKeys[keyName(msg)]; ok {
		return act(m, msg)
	}
	return nil, false
}

// chipPaste turns a file dropped into the prompt into one chip. It is
// one thing, not two hundred lines to scroll past; submit puts it back.
func (m *model) chipPaste(text string) (tea.Cmd, bool) {
	m.input.InsertString(m.storePaste(text))
	m.layout()
	m.notice.flash(fmt.Sprintf("pasted %s as a chip · it is sent in full", textfmt.Plural(strings.Count(text, "\n")+1, "line")))
	// A bracketed paste is one event, not a keystroke: the whole text
	// arrives at once, which is the moment @path is worth knowing about.
	m.observePaste(text)
	m.refresh(false)
	return nil, true
}

// handleDenyKey collects the optional reason for a denial: enter sends
// it, esc denies silently, anything else is text.
func (m *model) handleDenyKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch keyName(msg) {
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

// handlePendingKey answers the permission prompt. Every key is consumed:
// a run of characters typed at a prompt is ignored as a whole rather
// than answering with its first letter.
func (m *model) handlePendingKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch keyName(msg) {
	case "y", "Y", "enter":
		m.answer(agent.Allow, "")
	case "a", "A":
		m.answer(agent.AllowSession, "")
	case "r", "R":
		if m.pending.call.Rule == "" {
			break // no rule is offered for this call
		}
		m.noteUse("r")
		m.answer(agent.AllowAlways, "")
	case "n", "N":
		// Ask why so the model can adapt; enter with nothing skips.
		m.denying = true
		m.input.Reset()
		m.turn.setPhase("waiting for your reason")
		m.layout()
		m.refresh(false)
	case "esc":
		m.answer(agent.Deny, "")
	case "c", "C", "ctrl+c":
		// Not just this call: the whole turn stops here.
		m.answer(agent.Deny, agent.CancelledReason)
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

// keyAction handles one key press at the ordinary prompt. It is given
// the whole press because a few entries share a handler, and one of them
// hands the press on to the viewport. It reports whether it took the key; not taking it hands the press to the textarea, which
// is how a key that is only a shortcut in some states stays typeable in
// the others.
type keyAction func(*model, tea.KeyMsg) (tea.Cmd, bool)

// promptKeys is what each key does at the ordinary prompt — the same
// shape as the slash-command table, for the same reason: one long switch
// hid which keys existed, and every arm had to be read to find out.
var promptKeys = map[string]keyAction{
	"ctrl+c":    (*model).keyInterrupt,
	"ctrl+d":    (*model).keyEOF,
	"esc":       (*model).keyEscape,
	"ctrl+o":    (*model).keyFold,
	"backspace": (*model).keyBackspace,
	"ctrl+g":    (*model).keyBackground,
	"ctrl+s":    (*model).keySteer,
	"ctrl+l":    (*model).keyClearScreen,
	"shift+tab": (*model).keyCycleMode,
	"ctrl+t":    (*model).keyAgents,
	"ctrl+v":    (*model).keyPasteImage,
	"tab":       (*model).keyComplete,
	"up":        (*model).keyUp,
	"down":      (*model).keyDown,
	"pgup":      (*model).keyPage,
	"pgdown":    (*model).keyPage,
	"ctrl+u":    (*model).keyPage,
	"ctrl+b":    (*model).keyPage,
	"ctrl+f":    (*model).keyPage,
	"enter":     (*model).keyEnter,
}

// keyInterrupt is ctrl+c in three stages: cancel a running turn, else
// clear typed text, else quit. A second press while a cancel is pending
// forces the quit.
func (m *model) keyInterrupt(tea.KeyMsg) (tea.Cmd, bool) {
	if m.turn.running && !m.turn.cancelling {
		m.cancelTurn()
		return nil, true
	}
	if !m.turn.running && m.input.Value() != "" {
		m.input.Reset()
		m.status = "input cleared · ctrl+c again to quit"
		m.quitArmedAt = time.Now()
		m.layout()
		m.refresh(false)
		return nil, true
	}
	// One press when idle arms the quit; a second within a couple of
	// seconds does it. An accidental ctrl+c must not close a session.
	if !m.turn.running && time.Since(m.quitArmedAt) > quitArmWindow {
		m.quitArmedAt = time.Now()
		m.status = "ctrl+c again to quit"
		m.refresh(false)
		return nil, true
	}
	return m.quit(), true
}

// keyEOF is ctrl+d: it quits, but only on an empty prompt.
func (m *model) keyEOF(tea.KeyMsg) (tea.Cmd, bool) {
	if m.input.Value() == "" {
		return m.quit(), true
	}
	return nil, false
}

// quit ends the session. The life context goes with it, so a turn or a
// background command still running is cancelled here rather than left to
// finish into a program that has gone; the window is already blank by
// then, so nothing it would have said is lost from the screen.
func (m *model) quit() tea.Cmd {
	m.quitting = true
	m.endLife()
	return tea.Quit
}

// keyEscape cancels the turn, leaves the history, or clears the input,
// whichever applies first.
func (m *model) keyEscape(tea.KeyMsg) (tea.Cmd, bool) {
	if m.turn.running {
		m.cancelTurn()
		m.restoreQueued()
		return nil, true
	}
	if draft, ok := m.hist.cancel(); ok {
		m.setInput(draft)
		m.refresh(false)
		return nil, true
	}
	if m.input.Value() != "" {
		m.input.Reset()
		m.layout()
		m.refresh(false)
		return nil, true
	}
	return nil, false
}

// keyFold is ctrl+o: fold or unfold the whole of the last turn.
func (m *model) keyFold(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+o")
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
}

// keyBackspace takes a paste chip whole: the chip is one thing, so
// backspace takes all of it or none.
func (m *model) keyBackspace(tea.KeyMsg) (tea.Cmd, bool) {
	v := m.input.Value()
	if !pasteMarkerRe.MatchString(v) {
		return nil, false
	}
	loc := pasteMarkerRe.FindAllStringIndex(v, -1)
	if loc[len(loc)-1][1] != len(v) {
		return nil, false
	}
	m.input.SetValue(v[:loc[len(loc)-1][0]])
	m.input.CursorEnd()
	m.layout()
	m.refresh(false)
	return nil, true
}

// keyBackground is ctrl+g. A build or a test run that turns out to be
// long used to leave only waiting or cancelling. Promotion keeps the
// work: the command becomes a job, and the turn carries on.
func (m *model) keyBackground(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+g")
	job, err := m.opts.Running.PromoteLatest()
	if err != nil {
		m.status = err.Error()
		m.refresh(false)
		return nil, true
	}
	m.note(fmt.Sprintf("moved to background as job %d · /jobs shows it", job))
	m.refresh(false)
	return nil, true
}

// keySteer is ctrl+s: say something to the turn that is running, rather
// than queueing it for the next one. The agent holds the note and hands
// it to the model before its next call.
func (m *model) keySteer(tea.KeyMsg) (tea.Cmd, bool) {
	text := strings.TrimSpace(m.input.Value())
	switch {
	case !m.turn.running:
		m.status = "ctrl+s steers a running turn; there is none · enter sends"
	case text == "":
		m.status = "type what the model should do differently, then ctrl+s"
	default:
		m.noteUse("ctrl+s")
		m.opts.Typed.Note(text)
		m.opts.Agent.Steer(m.inflatePastes(text))
		m.turn.steering = append(m.turn.steering, text)
		m.hist.remember(text)
		m.input.Reset()
		m.layout()
	}
	m.refresh(false)
	return nil, true
}

func (m *model) keyClearScreen(tea.KeyMsg) (tea.Cmd, bool) { return tea.ClearScreen, true }

func (m *model) keyCycleMode(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("shift+tab")
	m.cycleMode()
	return nil, true
}

func (m *model) keyAgents(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+t")
	m.agentsCommand("")
	m.refresh(false)
	return nil, true
}

// keyPasteImage is ctrl+v. Text paste arrives through the terminal on
// its own; ctrl+v grabs an image from the clipboard and attaches it as
// an @mention.
func (m *model) keyPasteImage(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+v")
	m.pasteImage()
	return nil, true
}

func (m *model) keyComplete(tea.KeyMsg) (tea.Cmd, bool) {
	if !m.complete() {
		return nil, false
	}
	m.layout()
	m.refresh(false)
	return nil, true
}

// keyUp moves the completion highlight, takes back the last queued
// message, or steps up through the history, in that order.
func (m *model) keyUp(tea.KeyMsg) (tea.Cmd, bool) {
	if m.moveMenu(-1) {
		return nil, true
	}
	if m.turn.running && len(m.turn.queued) > 0 && m.input.Value() == "" {
		// Take the last queued message back to edit it: queued text was
		// unreachable until the turn ended.
		m.input.SetValue(m.turn.queued[len(m.turn.queued)-1])
		m.turn.queued = m.turn.queued[:len(m.turn.queued)-1]
		m.input.CursorEnd()
		m.layout()
		m.status = "took the queued message back · enter queues it again, ctrl+s steers this turn"
		m.refresh(false)
		return nil, true
	}
	if m.input.Line() == 0 {
		if text, ok := m.hist.back(m.input.Value()); ok {
			m.setInput(text)
			return nil, true
		}
	}
	return nil, false
}

func (m *model) keyDown(tea.KeyMsg) (tea.Cmd, bool) {
	if m.moveMenu(1) {
		return nil, true
	}
	if m.input.Line() == m.input.LineCount()-1 {
		if text, ok := m.hist.forward(); ok {
			m.setInput(text)
			return nil, true
		}
	}
	return nil, false
}

// keyPage pages the transcript. With text in the input ctrl+u/b/f are
// the textarea's own readline keys (kill line, back/forward a
// character); they page only when there is nothing to edit.
func (m *model) keyPage(msg tea.KeyMsg) (tea.Cmd, bool) {
	if k := keyName(msg); k == "pgup" || k == "pgdown" || m.input.Value() == "" {
		return m.scroll(msg), true
	}
	return nil, false
}

// keyEnter takes the completion if one is highlighted, then sends what
// is in the box: a command, a shell line, a queued prompt, or a turn.
func (m *model) keyEnter(tea.KeyMsg) (tea.Cmd, bool) {
	if c, idx := m.menu(); len(c) > 0 && !m.comp.args && strings.TrimSpace(m.input.Value()) != c[idx].name {
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
	// for the prompt, not for the model; sending it costs a turn of the
	// model guessing what "y" means.
	if len(text) == 1 && strings.ContainsAny(strings.ToLower(text), "ynar") && time.Since(m.pendingClosedAt) < strayKeyWindow {
		m.input.Reset()
		m.layout()
		m.status = "ignored a stray " + text + " meant for the approval prompt"
		m.refresh(false)
		return nil, true
	}
	if m.turn.running && !strings.HasPrefix(text, "/") {
		// Queue it; finishTurn sends it once the model is done.
		m.turn.queued = append(m.turn.queued, text)
		m.hist.remember(text)
		m.input.Reset()
		m.layout()
		m.status = fmt.Sprintf("queued %d · sends when this turn ends · esc cancels the turn and restores it", len(m.turn.queued))
		m.refresh(false)
		return nil, true
	}
	m.input.Reset()
	m.layout()
	m.hist.remember(text)
	switch {
	case strings.HasPrefix(text, "/"):
		return m.command(text), true
	case strings.HasPrefix(text, "!"):
		return m.shell(strings.TrimSpace(text[1:])), true
	}
	return m.submit(text), true
}

// setInput replaces what is being typed with a recalled line, leaving
// the cursor at its end and the box sized for it.
func (m *model) setInput(text string) {
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.layout()
}

// quitArmWindow is how long a first ctrl+c when idle stays armed.
const quitArmWindow = 2 * time.Second

// strayKeyWindow is how long after a permission prompt closes a lone
// approval key is treated as meant for it.
const strayKeyWindow = 1500 * time.Millisecond

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
