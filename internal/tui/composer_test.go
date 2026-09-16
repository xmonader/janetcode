package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestComposerLines is a table-driven check of the pure, wrap-aware line
// count that drives the growing prompt box (see composerLines in
// layout.go). It must respect the box's width — a line that wraps at
// that width counts extra rows exactly like an explicit newline would —
// and never report fewer than one line, even for an empty value. The
// wrapped-width cases pin the vendored bubbles textarea's own word-wrap
// output for a fixed value and width, rather than a count this package
// computes itself, which is the behaviour composerLines is required to
// defer to.
func TestComposerLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		width int
		want  int
	}{
		{"empty value is one line, not zero", "", 40, 1},
		{"one short line that does not wrap", "hello", 40, 1},
		{"text that wraps at a narrow width", "one two three four five six seven eight", 10, 5},
		{"explicit newlines, no wrapping", "alpha\nbeta\ngamma", 40, 3},
		{"a wrapping line plus an explicit newline", "a very long first line that wraps around here\nsecond", 10, 8},
		{"width 0 is not ready yet", "alpha\nbeta", 0, minInputLines},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := composerLines(tc.value, tc.width); got != tc.want {
				t.Fatalf("composerLines(%q, %d) = %d, want %d", tc.value, tc.width, got, tc.want)
			}
		})
	}
}

// The answer is memoized on the last (value, width) pair, so both halves
// of the key have to be part of it: the same text at another width, and
// another text at the same width, must both be measured afresh.
func TestComposerLinesMemoKeyedOnValueAndWidth(t *testing.T) {
	const text = "one two three four five six seven eight"
	wide, narrow := composerLines(text, 40), composerLines(text, 10)
	if wide == narrow {
		t.Fatalf("same count at both widths: %d", wide)
	}
	if again := composerLines(text, 40); again != wide {
		t.Fatalf("width 40 = %d then %d", wide, again)
	}
	if got := composerLines("short", 40); got != 1 {
		t.Fatalf("a new value at the memoized width = %d, want 1", got)
	}
	if again := composerLines(text, 40); again != wide {
		t.Fatalf("width 40 = %d after another value, want %d", again, wide)
	}
}

// Measuring costs a quarter of a megabyte per call inside bubbles, and
// every keystroke asks several times over; the memo is what keeps that
// off the typing path.
func BenchmarkComposerLines(b *testing.B) {
	for i := 0; i < b.N; i++ {
		composerLines("a fairly ordinary prompt line that wraps a little", 60)
	}
}

// TestInputHeightGrowsAndClamps drives the real textarea (not just the
// pure helper) through a harness and checks model.inputHeight(): it
// starts at one line, grows a line at a time with explicit newlines,
// stops growing at inputLimit() once content exceeds it (the textarea's
// own internal scrolling takes over past that point, unchanged from
// before this feature), and returns to one line once the content is
// cleared.
func TestInputHeightGrowsAndClamps(t *testing.T) {
	h := newHarness(t)
	if got := h.m.inputHeight(); got != minInputLines {
		t.Fatalf("empty composer height = %d, want %d", got, minInputLines)
	}

	h.keys("first line")
	if got := h.m.inputHeight(); got != 1 {
		t.Fatalf("one short line height = %d, want 1", got)
	}

	newline := func() { h.update(tea.KeyMsg{Type: tea.KeyCtrlJ}) }
	for i, line := range []string{"second", "third", "fourth", "fifth", "sixth", "seventh"} {
		newline()
		h.keys(line)
		want := i + 2 // one line already, plus this one
		if want > h.m.inputLimit() {
			want = h.m.inputLimit()
		}
		if got := h.m.inputHeight(); got != want {
			t.Fatalf("after %q, height = %d, want %d", line, got, want)
		}
	}
	// Over the cap: the box has stopped growing, and the textarea itself
	// (not just the reported height) is capped there too.
	if got, want := h.m.inputHeight(), h.m.inputLimit(); got != want {
		t.Fatalf("height over the cap = %d, want the cap %d", got, want)
	}
	if got, want := h.m.input.Height(), h.m.inputLimit(); got != want {
		t.Fatalf("textarea height = %d, want the cap %d", got, want)
	}

	// Clearing shrinks it back to one line.
	h.m.input.Reset()
	h.m.layout()
	if got := h.m.inputHeight(); got != minInputLines {
		t.Fatalf("height after clearing = %d, want %d", got, minInputLines)
	}
}

// TestInputHeightCompactMode checks that a short window's tighter cap
// (see inputLimit) governs the growth the same way the normal cap does.
func TestInputHeightCompactMode(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 16}) // compactMode: cap is 3
	if lim := h.m.inputLimit(); lim != 3 {
		t.Fatalf("compact cap = %d, want 3", lim)
	}
	h.m.input.SetValue(strings.Join([]string{"one", "two", "three", "four", "five"}, "\n"))
	h.m.layout()
	if got, want := h.m.inputHeight(), 3; got != want {
		t.Fatalf("compact-mode height for 5 lines = %d, want the compact cap %d", got, want)
	}
	if got, want := h.m.input.Height(), 3; got != want {
		t.Fatalf("compact-mode textarea height = %d, want %d", got, want)
	}
}

// TestFooterHeightTracksInputHeight is the anti-jump contract in
// layout.go's own words: when the composer grows or shrinks, the footer
// reservation and the viewport move by exactly the same number of rows
// in opposite directions, and a transcript that was pinned to the
// bottom stays pinned.
func TestFooterHeightTracksInputHeight(t *testing.T) {
	h := newHarness(t)
	h.m.transcript.banner("filler line so the transcript can scroll")
	h.m.refresh(false)
	if !h.m.pinned {
		t.Fatalf("transcript did not start pinned")
	}

	footerBefore, vhBefore := h.m.footerHeight(), h.m.viewport.Height
	newline := func() { h.update(tea.KeyMsg{Type: tea.KeyCtrlJ}) }
	h.keys("one")
	newline()
	h.keys("two")
	newline()
	h.keys("three")

	grew := h.m.inputHeight() - minInputLines
	if grew <= 0 {
		t.Fatalf("composer did not grow: height = %d", h.m.inputHeight())
	}
	if got, want := h.m.footerHeight(), footerBefore+grew; got != want {
		t.Fatalf("footerHeight = %d, want %d (grew by %d)", got, want, grew)
	}
	if got, want := h.m.viewport.Height, vhBefore-grew; got != want {
		t.Fatalf("viewport height = %d, want %d (shrunk by %d)", got, want, grew)
	}
	if !h.m.pinned {
		t.Fatalf("transcript unpinned itself while the composer grew")
	}

	// Shrinking back (clearing, as on send) must restore both exactly.
	h.m.input.Reset()
	h.m.layout()
	h.m.refresh(false)
	if got := h.m.footerHeight(); got != footerBefore {
		t.Fatalf("footerHeight after clearing = %d, want %d", got, footerBefore)
	}
	if got := h.m.viewport.Height; got != vhBefore {
		t.Fatalf("viewport height after clearing = %d, want %d", got, vhBefore)
	}
	if !h.m.pinned {
		t.Fatalf("transcript unpinned itself after clearing")
	}
}
