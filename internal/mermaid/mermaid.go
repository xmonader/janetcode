package mermaid

import "github.com/charmbracelet/x/ansi"

// maxLines is the tallest diagram that is drawn. Past it the transcript
// would scroll for a screen and a half, and the source is easier to read.
const maxLines = 120

// minWidth is the narrowest terminal a diagram is drawn in. Below it
// even a three-box flowchart comes out as a column of truncated labels.
const minWidth = 40

// Render lays the body of a ```mermaid fence out as terminal lines.
//
// ok is false whenever the source is not in the supported subset, is
// larger than the caps, or would not fit the width — every one of which
// means the caller should show the source as a code block instead. It is
// a pure function of (src, width): the same pair always draws the same
// lines.
func Render(src string, width int) (lines []string, ok bool) {
	if width < minWidth {
		return nil, false
	}
	d, err := Parse(src)
	if err != nil {
		return nil, false
	}
	return Layout(d, width)
}

// Layout draws an already parsed diagram.
func Layout(d *Diagram, width int) (lines []string, ok bool) {
	if width < minWidth || !narrowText(d) {
		return nil, false
	}
	if d.Kind == Sequence {
		return drawSequence(d, width)
	}
	l, err := build(d)
	if err != nil {
		return nil, false
	}
	if d.Dir == LeftRight {
		return drawLR(l, width)
	}
	return drawTD(l, width)
}

// narrowText reports whether every label is made of characters that take
// exactly one terminal cell. The canvas holds one rune per cell and sizes
// boxes by rune count, so a CJK or emoji label (two cells a character)
// pushed its box's right border out of line and overran the width, and a
// combining mark or control character (none) pulled it in — or, for an
// escape, reached the terminal as one. Such a diagram is shown as its
// source, like any other the layout cannot draw faithfully.
func narrowText(d *Diagram) bool {
	one := func(s string) bool {
		for _, r := range s {
			if r >= 0x20 && r < 0x7f {
				continue
			}
			if ansi.StringWidth(string(r)) != 1 {
				return false
			}
		}
		return true
	}
	for _, n := range d.Nodes {
		if !one(n.Label) {
			return false
		}
	}
	for _, n := range d.Actors {
		if !one(n.Label) {
			return false
		}
	}
	for _, e := range d.Edges {
		if !one(e.Label) {
			return false
		}
	}
	for _, m := range d.Msgs {
		if !one(m.Label) {
			return false
		}
	}
	return true
}
