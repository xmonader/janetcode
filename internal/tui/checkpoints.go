package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// escRewindWindow is how soon a second esc on an idle, empty prompt has
// to follow the first to open the checkpoint list.
const escRewindWindow = 700 * time.Millisecond

// cmdCheckpoints opens the checkpoint list: every restore point the
// session holds, what it covers and whether it is complete. Enter on one
// previews putting the tree back to before that turn ("/undo turn n"),
// which "/undo apply" carries out.
func (m *model) cmdCheckpoints(text string, fields []string) (tea.Cmd, bool) {
	cmd := m.openCheckpoints()
	return cmd, m.picker != nil
}

func (m *model) openCheckpoints() tea.Cmd {
	list := m.opts.Agent.CheckpointList()
	if len(list) == 0 {
		m.note("no checkpoints yet: one is taken before the first change of every turn that changes something")
		m.refresh(false)
		return nil
	}
	root := m.opts.Cwd
	var rows []row
	for i := len(list) - 1; i >= 0; i-- {
		c := list[i]
		what := fmt.Sprintf("turn %d · %s ago", c.Turn, textfmt.Age(time.Since(c.When)))
		if c.Intent != "" {
			what += " · " + textfmt.Clip(c.Intent, 48)
		}
		r := row{key: fmt.Sprintf("%s %d %s %s", c.ID, c.Turn, c.Intent, strings.Join(c.Files, " ")), name: fmt.Sprint(c.Turn),
			cells: []string{c.ID, what, c.Coverage(root)}}
		if !c.Complete {
			r.tag = "incomplete"
		}
		rows = append(rows, r)
	}
	return m.openPicker(&picker{
		title: "Checkpoints",
		hint:  "enter previews restoring the tree to before that turn · /undo apply carries it out · esc closes",
		rows:  rows,
		choose: func(r row, _ string) tea.Cmd {
			m.closePicker()
			var n int
			if _, err := fmt.Sscan(r.name, &n); err != nil || n <= 0 {
				return nil
			}
			return m.undoTurnCommand(n, false)
		},
	})
}

// escRewind is esc on an idle, empty prompt: the first one says what a
// second would do (when there is anything to rewind to), and a second
// within escRewindWindow opens the checkpoint list.
func (m *model) escRewind() tea.Cmd {
	now := time.Now()
	if !m.lastEsc.IsZero() && now.Sub(m.lastEsc) < escRewindWindow {
		m.lastEsc = time.Time{}
		return m.openCheckpoints()
	}
	m.lastEsc = now
	if ag := m.opts.Agent; ag != nil && len(ag.Checkpoints()) > 0 {
		m.note("esc again to rewind to a checkpoint")
		m.refresh(false)
	}
	return nil
}
