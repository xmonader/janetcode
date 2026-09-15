package mermaid

// A sequence diagram is drawn as lanes: one box per actor at the top,
// a lifeline falling from each, and one arrow per message across them.
// Two rows per message — its text, then the arrow — so a conversation
// of a dozen messages still fits in a screen.

// gapSeq is the least space between two lifelines; a message widens the
// gap under it when its text needs the room.
const gapSeq = 4

func drawSequence(d *Diagram, width int) ([]string, bool) {
	n := len(d.Actors)
	boxW := make([]int, n)
	for i, a := range d.Actors {
		boxW[i] = runeLen(a.Label) + 4
	}
	gaps := make([]int, n) // gaps[i] is the space after actor i
	for i := range gaps {
		gaps[i] = gapSeq
	}
	// Widen the gaps a message spans until its text has room between the
	// two lifelines it joins.
	for _, m := range d.Msgs {
		lo, hi := m.From, m.To
		if lo > hi {
			lo, hi = hi, lo
		}
		span := 0
		for i := lo; i < hi; i++ {
			span += boxW[i]/2 + gaps[i] + boxW[i+1]/2
		}
		need := runeLen(m.Label) + 4
		if span >= need {
			continue
		}
		add := (need - span + (hi - lo) - 1) / (hi - lo)
		for i := lo; i < hi; i++ {
			gaps[i] += add
		}
	}
	x, cx := 0, make([]int, n)
	for i := range d.Actors {
		cx[i] = x + boxW[i]/2
		x += boxW[i] + gaps[i]
	}
	total := x - gaps[n-1]
	if total > width {
		return nil, false
	}
	h := 3 + 2*len(d.Msgs) + 1
	if h > maxLines {
		return nil, false
	}
	c := newCanvas(width, h)
	for i, a := range d.Actors {
		c.box(cx[i]-boxW[i]/2, 0, boxW[i], 3, a.Label)
	}
	for i := range d.Actors {
		c.vline(cx[i], 3, h-1)
	}
	for i, m := range d.Msgs {
		y := 4 + 2*i
		sx, tx := cx[m.From], cx[m.To]
		lo, hi := sx, tx
		if lo > hi {
			lo, hi = hi, lo
		}
		if m.Dashed {
			// A dashed reply is drawn as characters rather than as a
			// line, so it stays dashed where it crosses a lifeline.
			for px := lo + 1; px < hi; px++ {
				c.set(px, y, '╌')
			}
		} else {
			c.hline(lo+1, hi-1, y)
		}
		if tx > sx {
			c.set(tx, y, '▶')
		} else {
			c.set(tx, y, '◀')
		}
		if m.Label == "" {
			continue
		}
		room := hi - lo - 1
		s := fit(m.Label, room)
		if s == "" {
			continue
		}
		c.text(lo+1+(room-runeLen(s))/2, y-1, s)
	}
	return c.lines(), true
}
