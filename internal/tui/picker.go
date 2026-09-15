package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// filter replaces the plain word match when the list is more than a
	// flat list: the session picker keeps a match's parents so a fork is
	// never shown adrift from what it branched off (see
	// session.FilterLineage).
	filter func(rows []row, query string) []row
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
	match := matchRows
	if p.filter != nil {
		match = p.filter
	}
	out := match(p.rows, p.field.Value())
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
	if rows, idx := m.pickerRows(); len(rows) > 0 {
		if next, ok := listMove(keyName(msg), idx, len(rows), m.dialogStep(p.winRows)); ok {
			p.idx, p.idxFor = next, p.field.Value()
			m.refresh(false)
			return nil, true
		}
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
	add(m.styles.key.Render(p.title))
	if p.hint != "" {
		add(m.styles.dim.Render(p.hint))
	}
	add("")
	add(m.styles.accent.Render("› ") + p.field.View())
	add(m.styles.faint.Render(strings.Repeat("─", inner)))
	rows, idx := m.pickerRows()
	if len(rows) == 0 {
		add("  " + m.styles.dim.Render("nothing matches"))
	} else {
		budget := fitRows(height, len(lines)+5)
		p.winRows = budget
		lines = append(lines, renderRows(m.styles, rows, idx, inner, budget)...)
	}
	add("")
	help := "↑↓ move · enter select · esc close"
	if len(rows) == 0 && p.freeText && strings.TrimSpace(p.field.Value()) != "" {
		help = "enter uses what you typed · esc close"
	}
	add(m.styles.faint.Render(help))
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

// openSessionPicker lists the sessions recorded for this directory in a
// searchable list, with the title, how long ago it was touched and how
// many messages it holds. Forks are indented under the session they were
// copied from (see session.Lineage), so the list shows where a
// conversation came from; searching keeps a match's parents for that
// same reason. /resume <n|id> still works without it.
func (m *model) openSessionPicker() tea.Cmd {
	infos, err := session.List(dirOf(m.opts.Session), m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("list sessions: %v", err)
		m.refresh(false)
		return nil
	}
	cur := m.opts.Session.Meta.ID
	// A session with nothing in it is not worth reopening; this one is
	// listed, marked, so its own forks hang under it rather than reading
	// as forks of something that is not here.
	var keep []session.Info
	for _, in := range infos {
		if in.Messages > 0 || in.Meta.ID == cur {
			keep = append(keep, in)
		}
	}
	nodes := session.Lineage(keep, cur)
	var rows []row
	for _, n := range nodes {
		in := n.Info
		tag := n.Mark()
		if note := n.Note(); note != "" {
			tag = note
		}
		rows = append(rows, row{
			key:   n.SearchKey(),
			cells: []string{in.Meta.ID, textfmt.Age(time.Since(in.Updated)), textfmt.Plural(in.Messages, "msg"), n.Prefix() + in.Label()},
			tag:   tag,
			name:  in.Meta.ID,
		})
	}
	if len(rows) == 0 || (len(rows) == 1 && rows[0].name == cur) {
		m.transcript.info("no earlier sessions in " + shortPath(m.opts.Cwd))
		m.refresh(false)
		return nil
	}
	return m.openPicker(&picker{
		title:    "Reopen a session",
		hint:     "most recent activity first, forks under what they branched off · type to search titles and ids",
		rows:     rows,
		freeText: true,
		filter: func(rs []row, query string) []row {
			kept := session.FilterLineage(nodes, query)
			keep := make(map[string]bool, len(kept))
			context := make(map[string]bool, len(kept))
			for _, n := range kept {
				keep[n.Info.Meta.ID] = true
				context[n.Info.Meta.ID] = n.Context
			}
			var out []row
			for _, r := range rs {
				if !keep[r.name] {
					continue
				}
				if context[r.name] && r.tag == "" {
					r.tag = "parent of a match"
				}
				out = append(out, r)
			}
			return out
		},
		choose: func(r row, typed string) tea.Cmd {
			id := r.name
			if id == "" {
				id = typed
			}
			if id == cur {
				m.note("already in this session")
				m.refresh(false)
				return nil
			}
			m.resume(id)
			m.refresh(false)
			return nil
		},
	})
}
