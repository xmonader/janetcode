package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// headerView is the one-line header: the name and model on the left, the
// mode prefix, working directory and branch on the right. When they do not
// all fit the session title goes first, then the branch, then the path is
// shortened — nothing is ever cut mid-glyph or allowed to wrap.
func (m *model) headerView() string {
	// The shared left gutter indents the header's content by the same
	// margin the transcript and input box use. It goes into `left`, so the
	// header's own width arithmetic (which measures left) absorbs it.
	lead := ""
	if g := m.gutter(); g > 0 {
		lead = m.styles.header.Render(strings.Repeat(" ", g))
	}
	left := lead + m.styles.headerHi.Render(" "+brandSpark+" janetcode ") + m.styles.header.Render(m.headerModel())
	title := ""
	if m.opts.Session != nil && m.opts.Session.Meta.Title != "" {
		title = " · " + truncateLine(m.opts.Session.Meta.Title, max(12, m.width/3))
	}
	branch := ""
	if m.branch != "" {
		branch = " ⎇ " + m.branch
	}
	prefix := ""
	switch modes := m.opts.Agent.Settings(); {
	case modes.PlanMode:
		prefix = "PLAN · "
	case modes.ReadOnly:
		prefix = "READ-ONLY · "
	case modes.AutoApprove:
		prefix = "YOLO · "
	case modes.AutoMode:
		prefix = "AUTO · "
	case modes.AutoEdits:
		prefix = "EDITS · "
	}
	// minPath is the shortest useful path: fitPath never goes below it.
	const minPath = 10
	line := func(withTitle, withBranch bool) (string, bool) {
		t, b := "", ""
		if withTitle {
			t = title
		}
		if withBranch {
			b = branch
		}
		fixed := lipgloss.Width(left) + lipgloss.Width(t) + lipgloss.Width(prefix) + lipgloss.Width(b) + 2
		room := m.width - fixed
		if room < minPath {
			return "", false
		}
		cwd := m.opts.Cwd
		if m.opts.Workspace != nil {
			cwd = m.opts.Workspace.Label()
		}
		p := fitPath(shortPath(cwd), room)
		gap := m.width - fixed - lipgloss.Width(p) + 1
		if gap < 1 {
			return "", false
		}
		return m.styles.header.Width(m.width).MaxHeight(1).Render(
			left + m.styles.header.Render(t+strings.Repeat(" ", gap)+prefix+p+b+" ")), true
	}
	for _, drop := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		if s, ok := line(drop[0], drop[1]); ok {
			return s
		}
	}
	// Narrower than the name and the mode together: the mode without its
	// separator, and the whole line cut to the width.
	mode := strings.TrimSuffix(prefix, " · ")
	if mode != "" {
		mode = " " + mode
	}
	return m.styles.header.Width(m.width).MaxHeight(1).Render(fitLine(left+m.styles.header.Render(mode), m.width))
}

// fitPath shows a path in max columns by dropping leading components:
// …/scratchpad/tuiproj rather than the middle of a long directory name,
// which is what a character cut leaves.
func fitPath(p string, max int) string {
	if lipgloss.Width(p) <= max {
		return p
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	for keep := 2; keep >= 1; keep-- {
		if len(parts) <= keep {
			continue
		}
		c := "…/" + strings.Join(parts[len(parts)-keep:], "/")
		if lipgloss.Width(c) <= max {
			return c
		}
	}
	return tailPath(p, max)
}

// tailPath keeps the end of a path so it fits in max columns.
func tailPath(p string, max int) string {
	if max < 8 {
		max = 8
	}
	if lipgloss.Width(p) <= max {
		return p
	}
	r := []rune(p)
	// The test above is in columns and the cut below is in runes: a path
	// of wide (or combining) characters can be too wide to fit and still
	// have fewer runes than the budget, which used to index before the
	// start of the slice and panic.
	if keep := max - 1; keep < len(r) {
		r = r[len(r)-keep:]
	}
	// And the budget is in columns: max-1 wide runes are twice as wide
	// as the room, so the header, finding the path did not fit, dropped
	// it altogether. Runes come off the front until the tail fits.
	for len(r) > 0 && lipgloss.Width(string(r)) > max-1 {
		r = r[1:]
	}
	for i, c := range r {
		// Start at a path separator so the tail is whole components. The
		// index has to be counted in runes: strings.IndexRune answers in
		// bytes, which cut a multi-byte path in the wrong place.
		if c == '/' && i < len(r)-1 {
			r = r[i:]
			break
		}
	}
	return "…" + string(r)
}

func shortPath(p string) string {
	// The home directory as a whole component only: /home/ann is not the
	// home of /home/anna, which was shown as "~a".
	home, err := os.UserHomeDir()
	home = strings.TrimSuffix(home, string(filepath.Separator))
	if err != nil || home == "" {
		return p
	}
	if p == home || strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
