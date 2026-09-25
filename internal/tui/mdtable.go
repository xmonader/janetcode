package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// glamour draws a table across the whole width whatever it holds, with no
// outer border, so a two-column table of short words became a grid with
// its last column running to the edge of the terminal. A block that is a
// table is drawn here instead: as wide as its content, inside a rounded
// border, with the header in the accent. A table wider than the terminal
// is wrapped to fit, as glamour would.

// mdTable is a parsed GFM table.
type mdTable struct {
	header []string
	align  []lipgloss.Position
	rows   [][]string
}

var mdTableDelim = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

// parseMarkdownTable reads a block that is a table and nothing else: a
// header row, a delimiter row, then rows. Anything else is not one.
func parseMarkdownTable(src string) (mdTable, bool) {
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "|") || !mdTableDelim.MatchString(lines[1]) {
		return mdTable{}, false
	}
	t := mdTable{header: splitTableRow(lines[0])}
	for _, d := range splitTableRow(lines[1]) {
		d = strings.TrimSpace(d)
		switch {
		case strings.HasPrefix(d, ":") && strings.HasSuffix(d, ":"):
			t.align = append(t.align, lipgloss.Center)
		case strings.HasSuffix(d, ":"):
			t.align = append(t.align, lipgloss.Right)
		default:
			t.align = append(t.align, lipgloss.Left)
		}
	}
	if len(t.align) != len(t.header) {
		return mdTable{}, false
	}
	for _, l := range lines[2:] {
		if !strings.Contains(l, "|") {
			return mdTable{}, false
		}
		row := splitTableRow(l)
		// GFM: a short row is padded, a long one cut to the header.
		for len(row) < len(t.header) {
			row = append(row, "")
		}
		t.rows = append(t.rows, row[:len(t.header)])
	}
	return t, true
}

// splitTableRow splits a row on its unescaped bars, dropping the outer
// ones, and trims each cell.
func splitTableRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	if strings.HasSuffix(l, "|") && !strings.HasSuffix(l, `\|`) {
		l = l[:len(l)-1]
	}
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			inCode = !inCode
			cur.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

var (
	mdInlineCode = regexp.MustCompile("`([^`]+)`")
	mdStrong     = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
)

// tableCell styles a cell's inline markdown: code in the code colour,
// strong text bold. Other marks are left as written.
func (t *transcript) tableCell(s string) string {
	s = mdStrong.ReplaceAllStringFunc(s, func(m string) string {
		return lipgloss.NewStyle().Bold(true).Render(strings.Trim(m, "*_"))
	})
	return mdInlineCode.ReplaceAllStringFunc(s, func(m string) string {
		return t.styles.tool.Render(strings.Trim(m, "`"))
	})
}

// renderTable draws a table block, or returns "" for a block that is not
// one. width is the transcript's; the table sits at the document margin
// glamour gives every other block.
func (t *transcript) renderTable(src string, width int) string {
	md, ok := parseMarkdownTable(src)
	if !ok {
		return ""
	}
	header := make([]string, len(md.header))
	for i, h := range md.header {
		header[i] = t.tableCell(h)
	}
	tb := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(t.styles.faint).
		Headers(header...).
		StyleFunc(func(row, col int) lipgloss.Style {
			st := lipgloss.NewStyle().Padding(0, 1)
			if col < len(md.align) {
				st = st.Align(md.align[col])
			}
			if row == table.HeaderRow {
				return st.Inherit(t.styles.accent).Bold(true)
			}
			return st
		})
	for _, r := range md.rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = t.tableCell(c)
		}
		tb.Row(cells...)
	}
	const margin = 2
	out := tb.Render()
	if room := width - 2*margin; lipgloss.Width(out) > room && room > 0 {
		out = tb.Width(room).Wrap(true).Render()
	}
	return indent(out, strings.Repeat(" ", margin))
}
