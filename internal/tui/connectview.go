// Rendering the connect dialog: how much room it gets, the breadcrumb and
// context above the list, and laying the rows out in columns. What the
// dialog is for and what the keys do is in connect.go.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// fitRows is how many list lines a modal may draw when the body is
// height rows tall and chrome lines are spent on the title, the search
// field and the help line. A short terminal used to get the full
// twelve, which pushed the title and the search field off the top of
// the screen because lipgloss.Place centres what it is given.
// dialogStep is how far pgup and pgdown move in a modal list: the window
// the last frame actually fitted, never zero.
func (m *model) dialogStep(win int) int {
	if win < minDialogRows {
		return dialogRows
	}
	return win
}

// listMove is how the highlight moves in every modal list: up and down
// wrap around, pgup and pgdown move by the window the last frame drew.
// It reports whether the key was one of those, so the picker, /connect
// and the step dialog agree without saying it three times.
func listMove(key string, idx, n, step int) (next int, handled bool) {
	switch key {
	case "up", "ctrl+p":
		if n == 0 {
			return idx, true
		}
		return (idx - 1 + n) % n, true
	case "down", "ctrl+n":
		if n == 0 {
			return idx, true
		}
		return (idx + 1) % n, true
	case "pgup":
		return max(idx-step, 0), true
	case "pgdown":
		if n == 0 {
			return idx, true
		}
		return min(idx+step, n-1), true
	}
	return idx, false
}

func fitRows(height, chrome int) int {
	avail := height - chrome
	if avail < 1 {
		return 0
	}
	if avail > dialogRows {
		return dialogRows
	}
	return avail
}

// clipHeight keeps s to at most n lines so a modal never pushes the
// reserved footer off the terminal. lipgloss.Place centres a box and
// will emit more rows than height when the box is taller than the body.
func clipHeight(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}

// connectView draws the modal centred in the body area.
func (m *model) connectView(height int) string {
	c := m.connect
	w := min(dialogWidth, m.width-4)
	inner := w - 4 // border + padding
	c.field.Width = inner - 3
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }

	add(m.styles.key.Render(m.dialogTitle()) + "  " + m.breadcrumb())
	if ctx := m.dialogContext(); ctx != "" {
		add(m.styles.dim.Render(ctx))
	}
	add("")
	add(m.styles.accent.Render("› ") + c.field.View())
	add(m.styles.faint.Render(strings.Repeat("─", inner)))
	if c.problem != "" {
		// Wrapped, not cut: the end of a failure is what to do about it.
		for i, l := range strings.Split(lipgloss.NewStyle().Width(inner-2).Render(c.problem), "\n") {
			prefix := "  "
			if i == 0 {
				prefix = "✗ "
			}
			add(m.styles.err.Render(prefix + strings.TrimRight(l, " ")))
		}
	}
	switch c.step {
	case stepPick, stepModel:
		// Two border rows, the lines added above, the blank and the help
		// line below, and the "n of m" counter.
		budget := fitRows(height, len(lines)+5)
		m.connect.rows = budget
		lines = append(lines, m.listLines(inner, budget)...)
	case stepName:
		add(m.styles.dim.Render("Used as the provider's name in the config file and in --provider."))
	case stepURL:
		add(m.styles.dim.Render("Edit the URL if your server lives elsewhere; enter keeps it."))
	case stepKeys:
		n := len(splitKeys(c.field.Value()))
		have := len(c.prov.Keys())
		switch {
		case n > 0:
			add(m.styles.dim.Render(fmt.Sprintf("%s entered · several keys are rotated when one is rate limited", textfmt.Plural(n, "key"))))
		case have > 0:
			add(m.styles.dim.Render(fmt.Sprintf("%s already configured · enter keeps them, or paste new ones", textfmt.Plural(have, "key"))))
		default:
			add(m.styles.dim.Render("Keys are hidden while typing and stored in " + shortPath(m.opts.ConfigPath)))
		}
	}
	add("")
	add(m.styles.faint.Render(m.dialogHelp()))
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) dialogTitle() string {
	c := m.connect
	switch {
	case c.slot != "":
		return "Choose the " + c.slot + " model"
	case c.modelOnly:
		return "Choose a model"
	case c.step == stepPick:
		return "Connect to a provider"
	default:
		return "Connect · " + c.name
	}
}

func (m *model) breadcrumb() string {
	c := m.connect
	if c.modelOnly {
		return ""
	}
	steps := []struct {
		s connectStep
		l string
	}{{stepPick, "provider"}, {stepURL, "url"}, {stepKeys, "keys"}, {stepModel, "model"}}
	var parts []string
	for _, st := range steps {
		cur := st.s == c.step || (st.s == stepPick && c.step == stepName)
		if cur {
			parts = append(parts, m.styles.accent.Bold(true).Render(st.l))
		} else {
			parts = append(parts, m.styles.faint.Render(st.l))
		}
	}
	return strings.Join(parts, m.styles.faint.Render(" › "))
}

// dialogContext summarises what has been chosen so far.
func (m *model) dialogContext() string {
	c := m.connect
	if c.step == stepPick || c.step == stepName {
		return ""
	}
	parts := []string{c.prov.Type + " API"}
	if c.step > stepURL && c.prov.BaseURL != "" {
		parts = append(parts, c.prov.BaseURL)
	}
	if c.step > stepKeys {
		switch n := len(c.prov.Keys()); {
		case n == 1:
			parts = append(parts, "1 key")
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d keys", n))
		default:
			parts = append(parts, "no key")
		}
	}
	return strings.Join(parts, " · ")
}

func (m *model) listLines(inner, budget int) []string {
	c := m.connect
	rows, idx := m.visible()
	var out []string
	if len(rows) == 0 {
		switch {
		case c.step == stepModel && c.loading:
			out = append(out, "  "+m.styles.dim.Render(m.spinner.View()+" fetching models from "+hostOf(c.prov.BaseURL)+"…"))
		case c.step == stepModel && c.loadErr != "":
			for _, l := range strings.Split(lipgloss.NewStyle().Width(inner-2).Render(c.loadErr), "\n") {
				out = append(out, "  "+m.styles.warn.Render(strings.TrimRight(l, " ")))
			}
			out = append(out, "  "+m.styles.dim.Render("type the model id and press enter"))
		case c.step == stepPick && strings.HasPrefix(strings.TrimSpace(c.field.Value()), "/"):
			out = append(out, "  "+m.styles.dim.Render("enter runs "+strings.TrimSpace(c.field.Value())))
		case c.field.Value() != "" && c.problem == "":
			// With a problem shown above, "nothing matches" said it twice.
			out = append(out, "  "+m.styles.dim.Render("nothing matches"))
		}
		return out
	}
	return renderRows(m.styles, rows, idx, inner, budget)
}

// matchRows keeps the rows whose key contains every word of the query, so
// "gpt mini" finds a model however its parts are ordered.
func matchRows(src []row, query string) []row {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []row
	for _, r := range src {
		key := strings.ToLower(r.key)
		ok := true
		for _, w := range words {
			if !strings.Contains(key, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// fits reports whether a window starting at start still draws row idx
// within budget lines, counting the section headings it must emit.
func fits(rows []row, start, idx, budget int) bool {
	used, section := 0, ""
	if start > 0 {
		section = rows[start-1].section
	}
	for i := start; i < len(rows); i++ {
		if rows[i].section != "" && rows[i].section != section {
			section = rows[i].section
			used++
		}
		used++
		if used > budget {
			return false
		}
		if i == idx {
			return true
		}
	}
	return false
}

// renderRows draws a window of a row list with its sections, columns and
// highlight. Every searchable list in the interface goes through it.
// budget is the number of screen lines the window may occupy, section
// headings included, so a short terminal gets a shorter list rather than
// a box taller than the screen.
func renderRows(sty *styles, rows []row, idx, inner, budget int) []string {
	if budget <= 0 {
		budget = dialogRows
	}
	widths := columnWidths(rows, inner-4)
	// Section headings take a line too, so the window that holds the
	// highlight is found by walking the start back until idx is drawn.
	start := 0
	if idx >= budget {
		start = idx - budget + 1
	}
	for start > 0 && !fits(rows, start, idx, budget) {
		start++
	}
	var out []string
	section := ""
	if start > 0 {
		section = rows[start-1].section
	}
	shown := 0
	for i := start; i < len(rows) && len(out) < budget; i++ {
		r := rows[i]
		if r.section != "" && r.section != section {
			if len(out)+2 > budget {
				break // a heading with no row under it says nothing
			}
			section = r.section
			out = append(out, sty.section.Render(strings.ToUpper(section)))
		}
		shown++
		// The last column takes what is left after the tag.
		last := inner - 2
		for _, w := range widths[:len(widths)-1] {
			last -= w + 2
		}
		if r.tag != "" {
			last -= lipgloss.Width(r.tag) + 2
		}
		var cells []string
		for j, cell := range r.cells {
			if j < len(widths)-1 {
				cell = pad(cell, widths[j])
			} else {
				cell = pad(cell, max(last, 4))
			}
			switch {
			case i == idx && j == 0:
				cell = sty.selected.Render(cell)
			case j == 0:
				cell = sty.key.Render(cell)
			case j == len(r.cells)-1:
				cell = sty.faint.Render(cell)
			}
			cells = append(cells, cell)
		}
		line := strings.Join(cells, "  ")
		if r.tag != "" {
			line += "  " + sty.tag.Render(r.tag)
		}
		if i == idx {
			line = sty.accent.Render("▸ ") + line
		} else {
			line = "  " + line
		}
		out = append(out, truncateLine(line, inner))
	}
	if shown < len(rows) {
		out = append(out, sty.faint.Render(fmt.Sprintf("  %d of %d", idx+1, len(rows))))
	}
	return out
}

// columnWidths sizes every column but the last to its widest cell, capped
// so the row fits.
func columnWidths(rows []row, avail int) []int {
	if len(rows) == 0 {
		return nil
	}
	n := len(rows[0].cells)
	widths := make([]int, n)
	for _, r := range rows {
		for j, c := range r.cells {
			if j < n-1 {
				widths[j] = max(widths[j], lipgloss.Width(c))
			}
		}
	}
	cap := avail / max(n, 1)
	for j := range widths {
		widths[j] = min(widths[j], cap)
	}
	return widths
}

func pad(s string, w int) string {
	s = truncateLine(s, w)
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

func (m *model) dialogHelp() string {
	c := m.connect
	switch c.step {
	case stepPick:
		return "↑↓ move · enter select · esc close"
	case stepModel:
		rows, idx := m.visible()
		back := "esc back"
		if c.modelOnly {
			back = "esc close"
		}
		if len(rows) == 0 && strings.TrimSpace(c.field.Value()) != "" {
			return fmt.Sprintf("enter uses %q as the model id · %s", strings.TrimSpace(c.field.Value()), back)
		}
		if len(rows) > 0 {
			return "↑↓ move · enter uses " + rows[idx].name + " · " + back
		}
		return "↑↓ move · enter select · " + back
	default:
		return "enter continue · esc back"
	}
}
