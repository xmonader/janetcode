package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// Vim mode for the composer: the config's vim_mode or /vim turns it on,
// and the composer then has vim's two modes. Insert mode is the ordinary
// composer; esc leaves it for normal mode, where letters are motions and
// edits instead of text. Ctrl keys, enter and the arrows keep their
// meaning in both modes, so sending, cancelling and the history work as
// they always did. It is deliberately the small everyday subset — the
// motions and edits a prompt needs, not ex commands or macros.
type vimState struct {
	on      bool
	normal  bool   // false is insert mode
	pending string // an operator waiting for its motion: "d", "c", "ci", "di", "g"
	reg     string // the last deleted text, for p
	regLine bool   // reg was a whole line (dd), so p puts it below
	undo    []vimSnap
	// insertFrom is the composer as the current insert began, and
	// insertSaved says the command that began it (cw, cc, o, …) already
	// put its own undo step on the stack, so what is typed is part of that
	// change. A plain i, a, I or A saved nothing: leaving insert mode saves
	// insertFrom, so u takes back what was typed rather than skipping over
	// it to the edit before.
	insertFrom  vimSnap
	insertSaved bool
}

type vimSnap struct {
	text string
	pos  int
}

// vimUndoCap bounds the undo stack; a prompt is not a novel.
const vimUndoCap = 100

// setVim turns vim mode on or off; on starts in insert mode so typing
// carries on as before.
func (m *model) setVim(on bool) {
	m.vim = vimState{on: on, insertFrom: vimSnap{text: m.input.Value()}}
}

// vimKey handles a key in vim mode. In insert mode only esc is its: it
// goes to normal mode rather than cancelling the turn or clearing the
// input. In normal mode every single character is a command (an unknown
// one does nothing rather than typing itself), and everything else falls
// through to the prompt's ordinary keys.
func (m *model) vimKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if !m.vim.on {
		return nil, false
	}
	name := keyName(msg)
	if !m.vim.normal {
		if name == "esc" && !m.turn.running {
			m.vim.normal = true
			m.vim.pending = ""
			if !m.vim.insertSaved && m.input.Value() != m.vim.insertFrom.text {
				m.vimSave(m.vim.insertFrom)
			}
			text, pos := m.vimText()
			if pos > 0 && text[pos-1] != '\n' {
				m.vimMoveTo(pos - 1)
			}
			m.refresh(false)
			return nil, true
		}
		return nil, false
	}
	if msg.Paste {
		return nil, false // a paste is text, whatever mode
	}
	switch {
	case name == "esc":
		if m.vim.pending != "" {
			m.vim.pending = ""
			m.refresh(false)
			return nil, true
		}
		if m.turn.running {
			return nil, false // cancels the turn, as it always has
		}
		return nil, true // esc in normal mode is a no-op, not "clear the input"
	case name == "backspace", name == "ctrl+h":
		// ^H too: left to the textarea it deleted text in normal mode.
		m.vimCommand('h')
		return nil, true
	case msg.Type == tea.KeyRunes && !msg.Alt:
		for _, r := range msg.Runes {
			m.vimCommand(r)
		}
		m.vimClamp()
		m.layout()
		m.refresh(false)
		return nil, true
	case msg.Type == tea.KeySpace:
		m.vimCommand('l')
		return nil, true
	}
	return nil, false
}

// vimText is the composer's text as runes and the cursor's offset in it.
func (m *model) vimText() ([]rune, int) {
	text := []rune(m.input.Value())
	row := m.input.Line()
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	pos := 0
	for i, line := range strings.Split(string(text), "\n") {
		if i == row {
			return text, pos + min(col, len([]rune(line)))
		}
		pos += len([]rune(line)) + 1
	}
	return text, len(text)
}

// vimMoveTo puts the cursor at a rune offset without touching the text.
func (m *model) vimMoveTo(pos int) {
	text := []rune(m.input.Value())
	pos = max(0, min(pos, len(text)))
	row, col := 0, 0
	for _, r := range text[:pos] {
		if r == '\n' {
			row, col = row+1, 0
		} else {
			col++
		}
	}
	for m.input.Line() > row {
		m.input.CursorUp()
	}
	for m.input.Line() < row {
		// A soft-wrapped line takes one step per screen row; a step that
		// does not move at all is the last row of the text.
		line, off := m.input.Line(), m.input.LineInfo().RowOffset
		m.input.CursorDown()
		if m.input.Line() == line && m.input.LineInfo().RowOffset == off {
			break
		}
	}
	m.input.SetCursor(col)
}

// vimSet replaces the text and puts the cursor at pos, saving what was
// there for u.
func (m *model) vimSet(old []rune, oldPos int, text []rune, pos int) {
	m.vimSave(vimSnap{string(old), oldPos})
	m.input.SetValue(string(text))
	m.vimMoveTo(pos)
}

// vimSave puts one state on the undo stack.
func (m *model) vimSave(s vimSnap) {
	m.vim.undo = append(m.vim.undo, s)
	if len(m.vim.undo) > vimUndoCap {
		m.vim.undo = m.vim.undo[1:]
	}
}

// vimInsert leaves normal mode at pos. saved says the command already
// put its undo step on the stack (see insertFrom).
func (m *model) vimInsert(pos int, saved bool) {
	text, cur := m.vimText()
	m.vim.normal = false
	m.vim.insertFrom, m.vim.insertSaved = vimSnap{string(text), cur}, saved
	m.vimMoveTo(pos)
}

// vimClamp keeps the normal-mode cursor on a character: a motion that
// ran off the end of a line (w on its last word, dw of the last word)
// left it past the end, where x and D found nothing to act on.
func (m *model) vimClamp() {
	if !m.vim.normal {
		return
	}
	text, pos := m.vimText()
	if ls, le := lineBounds(text, pos); le > ls && pos > le-1 {
		m.vimMoveTo(le - 1)
	}
}

// runeClass is vim's idea of a word: a run of letters, digits and _, or
// a run of other non-blank characters. Blanks (and newlines) separate.
func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

// lineBounds is the start and end (exclusive, at the newline) of the line
// holding pos.
func lineBounds(text []rune, pos int) (int, int) {
	start := pos
	for start > 0 && text[start-1] != '\n' {
		start--
	}
	end := pos
	for end < len(text) && text[end] != '\n' {
		end++
	}
	return start, end
}

// wordForward is w: the start of the next word.
func wordForward(text []rune, pos int) int {
	if pos >= len(text) {
		return len(text)
	}
	c := runeClass(text[pos])
	i := pos
	for i < len(text) && c != 0 && runeClass(text[i]) == c {
		i++
	}
	for i < len(text) && runeClass(text[i]) == 0 {
		i++
	}
	return i
}

// wordBack is b: the start of this word, or of the previous one.
func wordBack(text []rune, pos int) int {
	i := pos - 1
	for i > 0 && runeClass(text[i]) == 0 {
		i--
	}
	if i <= 0 {
		return 0
	}
	c := runeClass(text[i])
	for i > 0 && runeClass(text[i-1]) == c {
		i--
	}
	return i
}

// wordEnd is e: the last character of this word, or of the next one.
func wordEnd(text []rune, pos int) int {
	i := pos + 1
	for i < len(text) && runeClass(text[i]) == 0 {
		i++
	}
	if i >= len(text) {
		return max(0, len(text)-1)
	}
	c := runeClass(text[i])
	for i+1 < len(text) && runeClass(text[i+1]) == c {
		i++
	}
	return i
}

// innerWord is iw: the run of one class around pos.
func innerWord(text []rune, pos int) (int, int) {
	if pos >= len(text) {
		return pos, pos
	}
	c := runeClass(text[pos])
	s, e := pos, pos
	for s > 0 && runeClass(text[s-1]) == c && text[s-1] != '\n' {
		s--
	}
	for e < len(text) && runeClass(text[e]) == c && text[e] != '\n' {
		e++
	}
	return s, e
}

// cut removes text[s:e], keeping it for p.
func (m *model) cut(text []rune, s, e int, line bool) []rune {
	m.vim.reg, m.vim.regLine = string(text[s:e]), line
	out := append([]rune{}, text[:s]...)
	return append(out, text[e:]...)
}

func splice(text []rune, at int, ins string) []rune {
	out := append([]rune{}, text[:at]...)
	out = append(out, []rune(ins)...)
	return append(out, text[at:]...)
}

// vimCommand runs one normal-mode character.
func (m *model) vimCommand(r rune) {
	text, pos := m.vimText()
	ls, le := lineBounds(text, pos)
	lastCol := max(ls, le-1) // normal mode sits on a character, not past it
	op := m.vim.pending
	m.vim.pending = ""

	switch op {
	case "g":
		if r == 'g' {
			m.vimMoveTo(0)
		}
		return
	case "ci", "di":
		if r != 'w' {
			return
		}
		s, e := innerWord(text, pos)
		m.vimSet(text, pos, m.cut(text, s, e, false), s)
		if op == "ci" {
			m.vimInsert(s, true)
		}
		return
	case "d", "c":
		switch r {
		case 'i':
			m.vim.pending = op + "i"
		case 'd', 'c':
			if string(r) != op {
				return
			}
			if op == "c" { // cc: empty the line, keep it
				m.vimSet(text, pos, m.cut(text, ls, le, false), ls)
				m.vimInsert(ls, true)
				return
			}
			// dd: the whole line and its newline.
			s, e := ls, le
			if e < len(text) {
				e++
			} else if s > 0 {
				s--
			}
			m.vim.reg, m.vim.regLine = string(text[ls:le])+"\n", true
			out := append(append([]rune{}, text[:s]...), text[e:]...)
			m.vimSet(text, pos, out, min(s, len(out)))
			if s > 0 && s == ls-1 { // deleted the last line: land on the one above
				ps, _ := lineBounds(out, s-1)
				m.vimMoveTo(ps)
			}
		case 'w', 'e':
			// cw is ce in vim; dw stops at the line's end.
			var e int
			if r == 'e' || op == "c" {
				e = min(wordEnd(text, pos)+1, len(text))
				if pos < len(text) && runeClass(text[pos]) != 0 && (pos+1 >= len(text) || runeClass(text[pos+1]) != runeClass(text[pos])) {
					e = pos + 1 // on a word's last letter, cw changes just it
				}
			} else {
				e = wordForward(text, pos)
			}
			e = min(e, max(le, pos))
			m.vimSet(text, pos, m.cut(text, pos, e, false), pos)
			if op == "c" {
				m.vimInsert(pos, true)
			}
		case '$':
			m.vimSet(text, pos, m.cut(text, pos, le, false), pos)
			if op == "c" {
				m.vimInsert(pos, true)
			}
		}
		return
	}

	switch r {
	case 'h':
		if pos > ls {
			m.vimMoveTo(pos - 1)
		}
	case 'l':
		if pos < lastCol {
			m.vimMoveTo(pos + 1)
		}
	case 'j', 'k':
		col := pos - ls
		var target int
		if r == 'j' {
			if le >= len(text) {
				return
			}
			ns, ne := lineBounds(text, le+1)
			target = min(ns+col, max(ns, ne-1))
		} else {
			if ls == 0 {
				return
			}
			ps, pe := lineBounds(text, ls-1)
			target = min(ps+col, max(ps, pe-1))
		}
		m.vimMoveTo(target)
	case '0':
		m.vimMoveTo(ls)
	case '^':
		i := ls
		for i < le && unicode.IsSpace(text[i]) {
			i++
		}
		m.vimMoveTo(i)
	case '$':
		m.vimMoveTo(lastCol)
	case 'w':
		m.vimMoveTo(wordForward(text, pos))
	case 'b':
		m.vimMoveTo(wordBack(text, pos))
	case 'e':
		m.vimMoveTo(wordEnd(text, pos))
	case 'G':
		s, _ := lineBounds(text, len(text))
		m.vimMoveTo(s)
	case 'g', 'd', 'c':
		m.vim.pending = string(r)
	case 'x':
		if pos < le {
			out := m.cut(text, pos, pos+1, false)
			_, ne := lineBounds(out, min(pos, len(out)))
			m.vimSet(text, pos, out, min(pos, max(ls, ne-1)))
		}
	case 'D', 'C':
		m.vimSet(text, pos, m.cut(text, pos, le, false), max(ls, pos-1))
		if r == 'C' {
			m.vimInsert(pos, true)
		}
	case 'i':
		m.vimInsert(pos, false)
	case 'a':
		m.vimInsert(min(pos+1, le), false)
	case 'I':
		i := ls
		for i < le && unicode.IsSpace(text[i]) {
			i++
		}
		m.vimInsert(i, false)
	case 'A':
		m.vimInsert(le, false)
	case 'o':
		m.vimSet(text, pos, splice(text, le, "\n"), le+1)
		m.vimInsert(le+1, true)
	case 'O':
		m.vimSet(text, pos, splice(text, ls, "\n"), ls)
		m.vimInsert(ls, true)
	case 'p':
		if m.vim.reg == "" {
			return
		}
		if m.vim.regLine {
			// Below this line: at the next line's start, or after a new
			// newline when this is the last line. Either way the put line
			// starts at le+1.
			ins, at := m.vim.reg, le+1
			if le >= len(text) {
				ins, at = "\n"+strings.TrimSuffix(ins, "\n"), le
			}
			m.vimSet(text, pos, splice(text, at, ins), le+1)
			return
		}
		at := min(pos+1, le)
		m.vimSet(text, pos, splice(text, at, m.vim.reg), at+len([]rune(m.vim.reg))-1)
	case 'u':
		n := len(m.vim.undo)
		if n == 0 {
			m.status = "vim: nothing to undo"
			return
		}
		s := m.vim.undo[n-1]
		m.vim.undo = m.vim.undo[:n-1]
		m.input.SetValue(s.text)
		m.vimMoveTo(s.pos)
	}
}

// vimCommandLine is /vim: toggle, or on/off.
func (m *model) vimCommandLine(arg string) {
	switch strings.TrimSpace(arg) {
	case "on":
		m.setVim(true)
	case "off":
		m.setVim(false)
	case "":
		m.setVim(!m.vim.on)
	default:
		m.transcript.errorf("/vim takes on or off, or nothing to toggle")
		return
	}
	if m.vim.on {
		m.note("vim mode on · esc for normal mode, i to insert · vim_mode in the config starts every session this way")
	} else {
		m.note("vim mode off")
	}
}

// vimChip is the mode indicator for the status line, "" when vim mode is
// off.
func (m *model) vimChip() string {
	if !m.vim.on {
		return ""
	}
	if m.vim.normal {
		p := "NORMAL"
		if m.vim.pending != "" {
			p += " " + m.vim.pending
		}
		return m.styles.key.Render(p)
	}
	return m.styles.faint.Render("INSERT")
}
