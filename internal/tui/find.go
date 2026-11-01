package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// findState remembers the last /find so a repeat continues where the
// previous one stopped rather than starting over at the top.
type findState struct {
	query string // last /find query, for repeats
	at    int    // line of the last hit, so repeats advance past it
}

// from is the line to search after, given where the viewport is now. A
// repeat continues past the previous hit even when the viewport could
// not scroll down to it (matches near the bottom); a new query starts
// from the top of the transcript.
func (f *findState) from(q string, offset int) int {
	switch {
	case q != f.query:
		f.query = q
		return -1
	case f.at > offset:
		return f.at
	}
	return offset
}

func (m *model) find(q string) tea.Cmd {
	from := m.finder.from(q, m.viewport.YOffset)
	lines := strings.Split(m.lastContent, "\n")
	needle := strings.ToLower(q)
	var hits []int
	for i, l := range lines {
		if strings.Contains(strings.ToLower(ansiSeq.ReplaceAllString(l, "")), needle) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		m.status = fmt.Sprintf("no match for %q", q)
		return nil
	}
	next := hits[0]
	idx := 0
	for i, h := range hits {
		if h > from {
			next, idx = h, i
			break
		}
	}
	m.finder.at = next
	m.viewport.SetYOffset(next)
	cmd := m.syncPinned()
	m.status = fmt.Sprintf("match %d/%d for %q on line %d · /find again for the next", idx+1, len(hits), q, next+1)
	return cmd
}
