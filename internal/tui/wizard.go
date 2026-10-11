package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// /connect had the only step machine in the interface: a search field, a
// highlighted list, a breadcrumb of steps and an esc that goes back one
// step rather than closing. wizard is that machine with the provider
// parts taken out — the steps are data, a step is either a list or a text
// entry, and what a step does with the answer is a function. It draws
// through the same row list and dialog box the picker and /connect use,
// so there is one modal look, one matcher and one set of keys.
//
// /mcp add is the first caller. /connect keeps its own file because its
// model step loads a list over the network while the dialog is open,
// which is a step machine plus an async list, not a step machine.

// Where a step can send the dialog next.
const (
	wizNext = -1 // the following step
	wizStay = -2 // this step again, with the field cleared
	wizDone = -3 // finished: run done
)

type wizardStep struct {
	// name labels the step in the breadcrumb.
	name string
	// hint is the line under the title saying what to type or choose.
	hint string
	// placeholder is the search field's placeholder.
	placeholder string
	// rows makes the step a list; nil makes it a text entry.
	rows []row
	// initial fills the field when the step opens.
	initial string
	// optional lets enter accept an empty entry.
	optional bool
	// enter is given the highlighted row (a zero row for a text step)
	// and the trimmed text, and returns where to go and what went wrong.
	enter func(r row, typed string) (next int, problem string)
}

type wizard struct {
	title string
	steps []wizardStep
	// at is the current step, trail the steps walked to reach it, so esc
	// backs out the way in.
	at    int
	trail []int
	field textinput.Model
	idx   int
	// idxFor is the search text the highlight belongs to.
	idxFor string
	// winRows is the list budget the last frame fitted, so pgup and
	// pgdown move by a screenful of the window actually drawn.
	winRows int
	problem string
	// done runs after the last step.
	done func() tea.Cmd
}

// openWizard shows a multi-step dialog over the transcript.
func (m *model) openWizard(w *wizard) tea.Cmd {
	w.field = textinput.New()
	w.field.Prompt = ""
	w.field.Focus()
	m.wiz = w
	m.input.Blur()
	m.enterWizardStep(0)
	return textinput.Blink
}

func (m *model) closeWizard() {
	m.wiz = nil
	m.input.Focus()
	m.refresh(false)
}

// enterWizardStep prepares the field for step at.
func (m *model) enterWizardStep(at int) {
	w := m.wiz
	w.at, w.idx, w.idxFor, w.problem = at, 0, "", ""
	s := w.step()
	w.field.SetValue(s.initial)
	w.field.CursorEnd()
	w.field.Placeholder = s.placeholder
	m.refresh(false)
}

func (w *wizard) step() wizardStep { return w.steps[w.at] }

// wizardRows are the rows of the current step matching the search field,
// and the highlight.
func (m *model) wizardRows() ([]row, int) {
	w := m.wiz
	out := matchRows(w.step().rows, w.field.Value())
	if v := w.field.Value(); v != w.idxFor {
		w.idxFor, w.idx = v, 0
	}
	if w.idx >= len(out) {
		w.idx = max(len(out)-1, 0)
	}
	return out, w.idx
}

// handleWizardKey drives the dialog; every key is consumed.
func (m *model) handleWizardKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	w := m.wiz
	key := keyName(msg)
	switch key {
	case "ctrl+c":
		m.closeWizard()
		return nil, true
	case "esc":
		if len(w.trail) == 0 {
			m.closeWizard()
			return nil, true
		}
		back := w.trail[len(w.trail)-1]
		w.trail = w.trail[:len(w.trail)-1]
		m.enterWizardStep(back)
		return nil, true
	case "enter":
		return m.wizardEnter(), true
	}
	if len(w.step().rows) > 0 {
		rows, idx := m.wizardRows()
		if next, ok := listMove(key, idx, len(rows), m.dialogStep(w.winRows)); ok {
			w.idx, w.idxFor = next, w.field.Value()
			m.refresh(false)
			return nil, true
		}
	}
	var cmd tea.Cmd
	w.field, cmd = w.field.Update(msg)
	w.problem = ""
	m.refresh(false)
	return cmd, true
}

func (m *model) wizardEnter() tea.Cmd {
	w := m.wiz
	s := w.step()
	typed := strings.TrimSpace(w.field.Value())
	var chosen row
	if len(s.rows) > 0 {
		rows, idx := m.wizardRows()
		if len(rows) == 0 {
			w.problem = "nothing matches"
			m.refresh(false)
			return nil
		}
		chosen = rows[idx]
	} else if typed == "" && !s.optional {
		w.problem = "this one is needed"
		m.refresh(false)
		return nil
	}
	next, problem := s.enter(chosen, typed)
	if problem != "" {
		w.problem = problem
		m.refresh(false)
		return nil
	}
	switch next {
	case wizStay:
		at := w.at
		m.enterWizardStep(at)
		return nil
	case wizDone:
		done := w.done
		m.closeWizard()
		if done != nil {
			return done()
		}
		return nil
	case wizNext:
		next = w.at + 1
	}
	if next >= len(w.steps) {
		done := w.done
		m.closeWizard()
		if done != nil {
			return done()
		}
		return nil
	}
	w.trail = append(w.trail, w.at)
	m.enterWizardStep(next)
	return nil
}

// wizardView draws the dialog in the box every modal shares.
func (m *model) wizardView(height int) string {
	w := m.wiz
	s := w.step()
	width := modalBoxWidth(m.width)
	inner := width - 4
	w.field.Width = inner - 3
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(m.styles.key.Render(w.title) + "  " + m.wizardCrumb())
	if s.hint != "" {
		add(m.styles.dim.Render(s.hint))
	}
	add("")
	add(m.styles.accent.Render("› ") + w.field.View())
	add(m.styles.faint.Render(strings.Repeat("─", inner)))
	if w.problem != "" {
		add(m.styles.err.Render("✗ " + w.problem))
	}
	if len(s.rows) > 0 {
		rows, idx := m.wizardRows()
		if len(rows) == 0 {
			add("  " + m.styles.dim.Render("nothing matches"))
		} else {
			budget := fitRows(height, len(lines)+5)
			w.winRows = budget
			lines = append(lines, renderRows(m.styles, rows, idx, inner, budget)...)
		}
	}
	add("")
	add(m.styles.faint.Render(m.wizardHelp()))
	box := m.styles.dialog.Width(width - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) wizardCrumb() string {
	w := m.wiz
	var parts []string
	for i, s := range w.steps {
		if s.name == "" {
			continue
		}
		if i == w.at {
			parts = append(parts, m.styles.accent.Bold(true).Render(s.name))
		} else {
			parts = append(parts, m.styles.faint.Render(s.name))
		}
	}
	return strings.Join(parts, m.styles.faint.Render(" › "))
}

func (m *model) wizardHelp() string {
	w := m.wiz
	back := "esc back"
	if len(w.trail) == 0 {
		back = "esc close"
	}
	if len(w.step().rows) > 0 {
		return "↑↓ move · enter select · " + back
	}
	if w.step().optional {
		return "enter continue · empty enter skips · " + back
	}
	return "enter continue · " + back
}
