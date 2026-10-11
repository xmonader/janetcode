package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPickerOnATinyTerminal: on a terminal a few columns wide the
// picker's inner width went negative and drawing it panicked in
// strings.Repeat, taking the whole session down on a resize.
func TestPickerOnATinyTerminal(t *testing.T) {
	h := newHarness(t)
	var rows []row
	for range 40 {
		rows = append(rows, row{key: "alpha", cells: []string{"alpha", "beta gamma"}, name: "alpha"})
	}
	h.m.openPicker(&picker{title: "Pick", rows: rows, choose: func(row, string) tea.Cmd { return nil }})
	for _, w := range []int{1, 3, 5, 8, 12, 20} {
		for _, ht := range []int{1, 3, 6, 10} {
			h.update(tea.WindowSizeMsg{Width: w, Height: ht})
			_ = h.m.View()
			for range 45 {
				h.update(tea.KeyMsg{Type: tea.KeyDown})
				_ = h.m.View()
			}
		}
	}
}
