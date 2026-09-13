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
	switch {
	case ag.PlanMode:
		ag.SetPlanMode(false)
		ag.ReadOnly = false
		ag.AutoApprove = false
		ag.AutoMode = true
	case ag.AutoMode:
		ag.AutoMode = false
		ag.AutoApprove = true
	case ag.AutoApprove:
		ag.AutoApprove = false
		ag.AutoMode = false
	default:
		ag.AutoApprove = false
		ag.AutoMode = false
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

func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.asking != nil {
		return m.handleAskKey(msg)
	}
	if m.overlay != "" {
		return m.handleOverlayKey(msg)
	}
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
		m.flashNotice(fmt.Sprintf("pasted %s as a chip · it is sent in full", textfmt.Plural(strings.Count(string(msg.Runes), "\n")+1, "line")))
		// A bracketed paste is one event, not a keystroke: the whole
		// text arrives at once, which is the moment @path is worth
		// knowing about.
		m.observePaste(string(msg.Runes))
		m.refresh(false)
		return nil, true
	}
	// Permission prompt takes over the keyboard.
	if m.pending != nil && m.denying {
		// Collecting an optional reason: enter sends it, esc denies silently.
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
	if m.pending != nil {
		switch keyName(msg) {
		case "y", "Y", "enter":
			m.answer(agent.Allow, "")
		case "a", "A":
			m.answer(agent.AllowSession, "")
		case "r", "R":
			m.noteUse("r")
			m.answer(agent.AllowAlways, "")
		case "n", "N":
			// Ask why so the model can adapt; enter with nothing skips.
			m.denying = true
			m.input.Reset()
			m.setPhase("waiting for your reason")
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
	switch keyName(msg) {
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
			m.noteUse("ctrl+s")
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
	case "shift+tab":
		m.noteUse("shift+tab")
		m.cycleMode()
		return nil, true
	case "ctrl+t":
		m.noteUse("ctrl+t")
		m.agentsCommand("")
		m.refresh(false)
		return nil, true
	case "ctrl+v":
		// Text paste arrives through the terminal on its own; ctrl+v grabs an
		// image from the clipboard and attaches it as an @mention.
		m.noteUse("ctrl+v")
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
		if k := keyName(msg); k == "pgup" || k == "pgdown" || m.input.Value() == "" {
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
