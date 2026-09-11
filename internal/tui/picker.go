package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/session"
)

// The /connect dialog had the only searchable list in the interface;
// every other list — sessions, models — was a numbered printout and a
// typed number. The list machinery (matchRows, renderRows, the dialog
// box) is now shared, and picker is the thin wrapper that turns it into a
// modal over the transcript: a search field, a highlighted row, and a
// function to call with whatever was chosen.
//
// Typed arguments keep working: /resume 3 and /resume <id> never open the
// picker, and the picker is only what you get when you ask for the list.
type picker struct {
	title  string
	hint   string // a line under the title saying what the list is for
	rows   []row
	field  textinput.Model
	idx    int
	idxFor string
	// winRows is the list budget the last frame fitted, so pgup and
	// pgdown move by a screenful of the window actually drawn.
	winRows int
	// choose is called with the highlighted row, or with a zero row and
	// the typed text when nothing matches and free text is allowed.
	choose func(r row, typed string) tea.Cmd
	// freeText allows enter with no match, handing over the typed text.
	freeText bool
}

// openPicker shows a searchable list over the transcript.
func (m *model) openPicker(p *picker) tea.Cmd {
	p.field = textinput.New()
	p.field.Prompt = ""
	p.field.Focus()
	m.picker = p
	m.input.Blur()
	m.refresh(false)
	return textinput.Blink
}

func (m *model) closePicker() {
	m.picker = nil
	m.input.Focus()
	m.refresh(false)
}

// pickerRows are the rows matching the search field, and the highlight.
func (m *model) pickerRows() ([]row, int) {
	p := m.picker
	out := matchRows(p.rows, p.field.Value())
	if v := p.field.Value(); v != p.idxFor {
		p.idxFor, p.idx = v, 0
	}
	if p.idx >= len(out) {
		p.idx = max(len(out)-1, 0)
	}
	return out, p.idx
}

// handlePickerKey drives the modal; every key is consumed.
func (m *model) handlePickerKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	p := m.picker
	switch keyName(msg) {
	case "esc", "ctrl+c":
		m.closePicker()
		return nil, true
	case "up", "down", "ctrl+p", "ctrl+n":
		rows, idx := m.pickerRows()
		if len(rows) > 0 {
			d := 1
			if keyName(msg) == "up" || keyName(msg) == "ctrl+p" {
				d = -1
			}
			p.idx = (idx + d + len(rows)) % len(rows)
			p.idxFor = p.field.Value()
			m.refresh(false)
		}
		return nil, true
	case "pgup", "pgdown":
		rows, idx := m.pickerRows()
		if len(rows) > 0 {
			if keyName(msg) == "pgup" {
				p.idx = max(idx-m.dialogStep(p.winRows), 0)
			} else {
				p.idx = min(idx+m.dialogStep(p.winRows), len(rows)-1)
			}
			p.idxFor = p.field.Value()
			m.refresh(false)
		}
		return nil, true
	case "enter":
		rows, idx := m.pickerRows()
		typed := strings.TrimSpace(p.field.Value())
		choose := p.choose
		switch {
		case len(rows) > 0:
			r := rows[idx]
			m.closePicker()
			return choose(r, typed), true
		case p.freeText && typed != "":
			m.closePicker()
			return choose(row{}, typed), true
		}
		return nil, true
	}
	var cmd tea.Cmd
	p.field, cmd = p.field.Update(msg)
	m.refresh(false)
	return cmd, true
}

// pickerView draws the modal, in the same box the connect dialog uses.
func (m *model) pickerView(height int) string {
	p := m.picker
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	p.field.Width = inner - 3
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(styleKey.Render(p.title))
	if p.hint != "" {
		add(styleDim.Render(p.hint))
	}
	add("")
	add(styleAccent.Render("› ") + p.field.View())
	add(styleFaint.Render(strings.Repeat("─", inner)))
	rows, idx := m.pickerRows()
	if len(rows) == 0 {
		add("  " + styleDim.Render("nothing matches"))
	} else {
		budget := fitRows(height, len(lines)+5)
		p.winRows = budget
		lines = append(lines, renderRows(rows, idx, inner, budget)...)
	}
	add("")
	help := "↑↓ move · enter select · esc close"
	if len(rows) == 0 && p.freeText && strings.TrimSpace(p.field.Value()) != "" {
		help = "enter uses what you typed · esc close"
	}
	add(styleFaint.Render(help))
	box := styleDialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

// openSessionPicker lists the sessions recorded for this directory in a
// searchable list, with the title, how long ago it was touched and how
// many messages it holds. /resume <n|id> still works without it.
func (m *model) openSessionPicker() tea.Cmd {
	infos, err := session.List(dirOf(m.opts.Session), m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("list sessions: %v", err)
		m.refresh(false)
		return nil
	}
	var rows []row
	for _, in := range infos {
		if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
			continue
		}
		label := in.Label()
		mark := ""
		if in.Meta.Settled {
			mark = "settled"
		}
		rows = append(rows, row{
			key:   in.Meta.ID + " " + label,
			cells: []string{in.Meta.ID, shortAge(time.Since(in.Updated)), plural(in.Messages, "msg"), label},
			tag:   mark,
			name:  in.Meta.ID,
		})
	}
	if len(rows) == 0 {
		m.transcript.info("no earlier sessions in " + shortPath(m.opts.Cwd))
		m.refresh(false)
		return nil
	}
	return m.openPicker(&picker{
		title:    "Reopen a session",
		hint:     "most recent activity first · type to search titles and ids",
		rows:     rows,
		freeText: true,
		choose: func(r row, typed string) tea.Cmd {
			id := r.name
			if id == "" {
				id = typed
			}
			m.resume(id)
			m.refresh(false)
			return nil
		},
	})
}
