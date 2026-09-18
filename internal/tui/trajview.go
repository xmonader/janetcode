package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trajectory"
)

// trajBrowse is the trajectory browser's own state: which projection the
// strip draws, which records the list is limited to, and whether the
// list is in ledger order or slowest-first. Everything that is just
// "where the highlight is" rides on overlayState, like the other panels.
type trajBrowse struct {
	mode   trajectory.Mode
	filter string // grep terms limiting the list; empty is everything
	slow   bool   // slowest-first ordering
	listH  int    // the list rows the last frame showed, for pgup/pgdown
}

// openTrajectory puts the ledger browser up, fresh except for the
// projection the reader last chose.
func (m *model) openTrajectory() {
	m.traj.filter, m.traj.slow = "", false
	m.ov.open(overlayTrajectory)
	m.ov.follow = false // the list reads from the top, like the ledger
	m.input.Blur()
	m.refresh(false)
}

// trajLedger is the session folded for this frame. The browser is a
// live panel, so the fold runs per View and a turn in progress shows up
// while it happens.
func (m *model) trajLedger() trajectory.Ledger {
	return trajectory.Fold(m.sessionEvents())
}

// trajRow is one line of the browser's list: a ledger record, or a
// heading with record == 0 that the highlight steps over.
type trajRow struct {
	record int // Record.Index; 0 means a heading
	text   string
}

func (m *model) trajRows(l trajectory.Ledger, width int) []trajRow {
	var out []trajRow
	add := func(record int, text string) { out = append(out, trajRow{record, text}) }
	switch {
	case m.traj.slow:
		rows := trajectory.Slowest(l, 0)
		if m.traj.filter != "" {
			rows = trajectory.Search(trajectory.Ledger{All: rows}, m.traj.filter)
		}
		add(0, m.styles.key.Render("slowest first")+"  "+m.styles.dim.Render(textfmt.Plural(len(rows), "record")))
		for _, r := range rows {
			add(r.Index, trajectory.Line(r, width))
		}
	case m.traj.filter != "":
		hits := trajectory.Search(l, m.traj.filter)
		add(0, m.styles.key.Render(textfmt.Plural(len(hits), "record"))+"  "+
			m.styles.dim.Render("matching "+strconv.Quote(m.traj.filter)+" · c clears"))
		for _, r := range hits {
			add(r.Index, trajectory.Line(r, width))
		}
	default:
		for ti, t := range l.Turns {
			if ti > 0 {
				add(0, "")
			}
			add(0, trajectory.TurnHeading(t))
			for _, g := range t.Groups {
				if note := trajectory.GroupHeading(g); note != "" {
					add(0, "  "+m.styles.dim.Render(note))
				}
				for _, r := range g.Records {
					add(r.Index, trajectory.Line(r, width))
				}
			}
		}
	}
	return out
}

// trajSelectable is the positions of the record rows inside rows — the
// headings the highlight may not land on.
func trajSelectable(rows []trajRow) []int {
	var out []int
	for i, r := range rows {
		if r.record != 0 {
			out = append(out, i)
		}
	}
	return out
}

func (m *model) handleTrajKey(msg tea.KeyMsg) tea.Cmd {
	if m.ov.child != "" {
		return m.handleTrajDetailKey(msg)
	}
	l := m.trajLedger()
	w := max(m.width-6, 40)
	rows := m.trajRows(l, w)
	sel := trajSelectable(rows)
	m.ov.clampIdx(len(sel))
	switch k := keyName(msg); {
	case k == "esc", k == "ctrl+c":
		m.closeOverlay()
	case k == "enter":
		if m.ov.idx < len(sel) {
			m.ov.openChild(strconv.Itoa(rows[sel[m.ov.idx]].record))
			m.ov.follow = false // an inspected record reads from its head
			m.refresh(false)
		}
	case k == "m":
		m.traj.mode = (m.traj.mode + 1) % 4
		m.refresh(false)
	case k == "s":
		m.traj.slow = !m.traj.slow
		m.ov.idx, m.ov.scroll = 0, 0
		m.refresh(false)
	case k == "c":
		if m.traj.filter != "" {
			m.traj.filter = ""
			m.ov.idx, m.ov.scroll = 0, 0
			m.refresh(false)
		}
	case k == "/":
		return m.openTrajSearch(l)
	case k == "g":
		m.ov.idx, m.ov.scroll = 0, 0
		m.refresh(false)
	case k == "G":
		m.ov.idx = max(len(sel)-1, 0)
		m.refresh(false)
	default:
		if m.trajListMove(k, len(sel)) {
			m.refresh(false)
		}
	}
	return nil
}

// trajListMove steps the highlight over the record rows, a row or a
// screenful at a time, keeping the landed-on row inside the window that
// ov.scroll tops.
func (m *model) trajListMove(k string, n int) bool {
	if n == 0 {
		return false
	}
	step := 0
	switch k {
	case "up", "k", "ctrl+p":
		step = -1
	case "down", "j", "ctrl+n":
		step = 1
	case "pgup":
		step = -m.trajListH()
	case "pgdown":
		step = m.trajListH()
	default:
		return false
	}
	m.ov.idx = (m.ov.idx + step + n) % n
	return true
}

func (m *model) handleTrajDetailKey(msg tea.KeyMsg) tea.Cmd {
	switch k := keyName(msg); {
	case k == "esc":
		m.ov.closeChild()
		m.refresh(false)
	case k == "ctrl+c":
		m.closeOverlay()
	case m.ov.paneKey(k, m.paneHeight()):
		m.refresh(false)
	}
	return nil
}

// openTrajSearch is the ledger's search: every record in a pickable
// list, matching as you type. Enter keeps the words as the list's
// filter and lands the highlight on the record that was chosen.
func (m *model) openTrajSearch(l trajectory.Ledger) tea.Cmd {
	w := max(m.width-10, 40)
	var rows []row
	for _, r := range l.All {
		rows = append(rows, row{
			key:   trajSearchKey(r),
			cells: []string{trajectory.Line(r, w)},
			name:  strconv.Itoa(r.Index),
		})
	}
	if len(rows) == 0 {
		m.note("no records to search yet")
		m.refresh(false)
		return nil
	}
	return m.openPicker(&picker{
		title:    "Search the trajectory",
		hint:     "matches arguments, results, answers, tools — what the ledger holds, not just what it showed",
		rows:     rows,
		freeText: true,
		choose: func(r row, typed string) tea.Cmd {
			m.traj.filter = typed
			m.traj.slow = false
			m.ov.idx, m.ov.scroll = 0, 0
			if r.name != "" {
				want := atoi(r.name)
				rows := m.trajRows(m.trajLedger(), max(m.width-6, 40))
				for i, pos := range trajSelectable(rows) {
					if rows[pos].record == want {
						m.ov.idx = i
						break
					}
				}
			}
			m.refresh(false)
			return nil
		},
	})
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func trajSearchKey(r trajectory.Record) string {
	var b strings.Builder
	for _, s := range []string{r.Text, r.Preview, r.Result, r.Tool, r.Intent, r.Model, r.Detail, r.Output} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// trajListH is how far pgup and pgdown step: the record rows the last
// frame showed, latched like the picker's winRows.
func (m *model) trajListH() int {
	if m.traj.listH < 4 {
		return 10
	}
	return m.traj.listH
}

func (m *model) trajListView(height int) string {
	w := max(m.width-2, 20)
	l := m.trajLedger()
	if len(l.All) == 0 {
		return m.trajFrame(height, w, []string{
			m.styles.key.Render("Trajectory"),
			"",
			m.styles.dim.Render("no trajectory yet: no turn has run in this session"),
			m.styles.faint.Render("esc close"),
		})
	}
	var lines []string
	u := l.Usage()
	head := m.styles.key.Render("Trajectory") + "  " +
		m.styles.dim.Render(textfmt.Plural(len(l.All), "record")+" · "+textfmt.Plural(len(l.Turns), "turn"))
	if !u.Empty() {
		head += m.styles.dim.Render(" · ↑" + textfmt.Tokens(u.Input+u.CacheRead+u.CacheWrite) +
			" ↓" + textfmt.Tokens(u.Output))
		if u.CostUSD > 0 {
			head += m.styles.dim.Render(fmt.Sprintf(" · $%.4f", u.CostUSD))
		}
	}
	lines = append(lines, head)
	lines = append(lines, m.styles.faint.Render("↑↓ move · enter inspect · / search · s slowest · m projection · esc close"))
	lines = append(lines, m.trajStrip(w)...)
	lines = append(lines, "")

	rows := m.trajRows(l, w-2)
	sel := trajSelectable(rows)
	if m.ov.idx >= len(sel) {
		m.ov.idx = max(len(sel)-1, 0)
	}
	selectedRow := -1
	if m.ov.idx < len(sel) {
		selectedRow = sel[m.ov.idx]
	}
	// The window: keep the highlighted row visible, clamp at both ends.
	viewH := max(height-len(lines)-2, 4)
	m.traj.listH = viewH
	if selectedRow >= 0 {
		if selectedRow < m.ov.scroll {
			m.ov.scroll = selectedRow
		}
		if selectedRow >= m.ov.scroll+viewH {
			m.ov.scroll = selectedRow - viewH + 1
		}
	}
	maxScroll := max(len(rows)-viewH, 0)
	if m.ov.scroll > maxScroll {
		m.ov.scroll = maxScroll
	}
	shown := rows[m.ov.scroll:min(m.ov.scroll+viewH, len(rows))]
	for i, r := range shown {
		abs := m.ov.scroll + i
		if r.record == 0 {
			lines = append(lines, truncateLine(r.text, w))
			continue
		}
		mark := "  "
		if abs == selectedRow {
			mark = m.styles.accent.Render("▸ ")
		}
		lines = append(lines, truncateLine(mark+r.text, w))
	}
	return m.trajFrame(height, w, lines)
}

func (m *model) trajFrame(height, w int, lines []string) string {
	return clipHeight(strings.Join(lines, "\n"), height)
}

// trajStrip is the timing overview: one ruler with the turn boundaries,
// then one row per lane that has anything on it.
func (m *model) trajStrip(w int) []string {
	tl, ok := trajectory.TimelineOf(m.trajLedger(), m.traj.mode)
	if !ok {
		return []string{m.styles.dim.Render("no clock on these records for the " + m.traj.mode.String() + " projection · m cycles")}
	}
	var legend strings.Builder
	legend.WriteString(m.styles.faint.Render(m.traj.mode.String()))
	switch m.traj.mode {
	case trajectory.ModeSequence:
		legend.WriteString(m.styles.faint.Render(" · one unit per record"))
	default:
		legend.WriteString(m.styles.faint.Render(" · " + textfmt.Duration(time.Duration(tl.End-tl.Start)*time.Millisecond)))
	}
	lanes := []struct {
		no    int
		name  string
		style lipgloss.Style
	}{{0, "input", m.styles.user}, {1, "model", m.styles.accent}, {2, "tools", m.styles.tool}}
	present := map[int]bool{}
	for _, s := range tl.Spans {
		present[s.Lane] = true
	}
	first := true
	for _, lane := range lanes {
		if !present[lane.no] {
			continue
		}
		if !first {
			legend.WriteString(m.styles.faint.Render("  ·  "))
		}
		first = false
		legend.WriteString(lane.style.Render("█") + " " + lane.name)
	}
	out := []string{legend.String(), m.trajRuler(tl, w)}
	for _, lane := range lanes {
		if present[lane.no] {
			out = append(out, m.trajLane(tl, lane.no, w))
		}
	}
	return out
}

// trajRuler is the boundary row: a tick per turn start, with the turn
// number where it fits.
func (m *model) trajRuler(tl trajectory.Timeline, w int) string {
	if w < 4 || tl.End <= tl.Start {
		return ""
	}
	ticks := make([]rune, w)
	for i := range ticks {
		ticks[i] = ' '
	}
	for _, b := range tl.Boundary {
		p := int((b.Time - tl.Start) / (tl.End - tl.Start) * float64(w))
		if p < 0 {
			p = 0
		}
		if p >= w {
			p = w - 1
		}
		ticks[p] = '│'
	}
	var labels strings.Builder
	last := 0
	for _, b := range tl.Boundary {
		p := int((b.Time - tl.Start) / (tl.End - tl.Start) * float64(w))
		if p < 0 {
			p = 0
		}
		if p >= w {
			p = w - 1
		}
		label := "t" + strconv.Itoa(b.Turn)
		if p < last || p+len(label) > w {
			continue
		}
		for labels.Len() < p {
			labels.WriteByte(' ')
		}
		labels.WriteString(label)
		last = p + len(label) + 1
	}
	return m.styles.faint.Render(labels.String()) + "\n" + m.styles.dim.Render(string(ticks))
}

// trajLane renders one lane as runs of same-colour cells: █ where a
// span covers the cell, ░ where the lane is idle. Runs are styled as
// units, not cells, so the strip stays cheap at full width.
func (m *model) trajLane(tl trajectory.Timeline, lane, w int) string {
	cells := make([]trajectory.Span, w)
	have := make([]bool, w)
	for i := 0; i < w; i++ {
		a := tl.Start + (tl.End-tl.Start)*float64(i)/float64(w)
		b := tl.Start + (tl.End-tl.Start)*float64(i+1)/float64(w)
		var pick *trajectory.Span
		var bestCover float64
		for si := range tl.Spans {
			s := &tl.Spans[si]
			if s.Lane != lane || s.Start >= b || s.End < a {
				continue
			}
			if cover := min(s.End, b) - max(s.Start, a); cover > bestCover {
				pick, bestCover = s, cover
			}
		}
		if pick != nil {
			cells[i] = *pick
			have[i] = true
		}
	}
	var out strings.Builder
	i := 0
	for i < w {
		if !have[i] {
			j := i
			for j < w && !have[j] {
				j++
			}
			out.WriteString(m.styles.faint.Render(strings.Repeat("░", j-i)))
			i = j
			continue
		}
		s := cells[i]
		j := i
		for j < w && have[j] && cells[j].Index == s.Index {
			j++
		}
		out.WriteString(m.trajKindStyle(s).Render(strings.Repeat("█", j-i)))
		i = j
	}
	return out.String()
}

func (m *model) trajKindStyle(s trajectory.Span) lipgloss.Style {
	if s.IsError {
		return m.styles.err
	}
	switch s.Kind {
	case trajectory.KindMessage, trajectory.KindCompacted, trajectory.KindVerdict:
		return m.styles.accent
	case trajectory.KindTool:
		return m.styles.tool
	default:
		return m.styles.user
	}
}

func (m *model) trajDetailView(height int) string {
	l := m.trajLedger()
	r, ok := l.At(atoi(m.ov.child))
	if !ok {
		m.ov.closeChild()
		return m.trajListView(height)
	}
	w := max(m.width-2, 20)
	head := fmt.Sprintf("#%d  %s", r.Index, m.styles.dim.Render(fmt.Sprintf("turn %d · step %d", r.Turn, r.Step)))
	help := m.styles.faint.Render("esc back to the ledger · ↑↓ / pgup pgdn scroll · ctrl+c close")
	body := strings.Split(trajectory.Inspect(l, r.Index), "\n")
	for i, line := range body {
		body[i] = truncateLine(line, w)
	}
	viewH := max(height-3, 1)
	maxScroll := max(len(body)-viewH, 0)
	if m.ov.scroll > maxScroll {
		m.ov.scroll = maxScroll
	}
	shown := body[m.ov.scroll:min(m.ov.scroll+viewH, len(body))]
	parts := []string{truncateLine(head, w), help, ""}
	parts = append(parts, shown...)
	return clipHeight(strings.Join(parts, "\n"), height)
}
