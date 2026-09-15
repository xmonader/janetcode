package mermaid

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
	if width < minWidth {
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
