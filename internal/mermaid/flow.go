package mermaid

// Drawing a layered graph: boxes first, then the lines between them,
// then the edge labels — in that order, so a label only lands where
// nothing has been drawn yet and is dropped rather than written over a
// line. Top-down and left-right are two routines rather than one
// transposed one: the corner characters, the arrowheads and where a
// label fits differ enough that sharing them would cost more than it
// saves.

const (
	bandTD  = 3 // rows between two ranks
	gapTD   = 3 // columns between two boxes on the same rank
	gapLR   = 1 // rows between two boxes in the same column
	minBand = 4 // columns between two columns, before label room
)

type labelAt struct {
	x, y int
	text string
}

// drawTD lays the ranks out as rows. It returns the lines, or ok=false
// when the result would be wider than the terminal.
func drawTD(l *layered, width int) ([]string, bool) {
	total := 0
	for _, r := range l.ranks {
		w := 0
		for i, v := range r {
			if i > 0 {
				w += gapTD
			}
			w += l.nodes[v].w
		}
		if w > total {
			total = w
		}
	}
	if total > width {
		return nil, false
	}
	for ri, r := range l.ranks {
		w := 0
		for i, v := range r {
			if i > 0 {
				w += gapTD
			}
			w += l.nodes[v].w
		}
		x := (total - w) / 2
		for _, v := range r {
			l.nodes[v].x = x
			l.nodes[v].y = ri * (3 + bandTD)
			x += l.nodes[v].w + gapTD
		}
	}
	h := len(l.ranks)*(3+bandTD) - bandTD
	if h > maxLines {
		return nil, false
	}
	c := newCanvas(width, h)
	for _, n := range l.nodes {
		if n.dummy {
			c.vline(n.x, n.y, n.y+n.h-1)
			continue
		}
		c.box(n.x, n.y, n.w, n.h, n.label)
	}
	for _, s := range l.segs {
		a, b := l.nodes[s.from], l.nodes[s.to]
		sx, tx := a.x+a.w/2, b.x+b.w/2
		y0 := a.y + a.h
		y1, y2 := y0+1, y0+2
		c.vline(sx, y0, y1)
		c.hline(sx, tx, y1)
		c.vline(tx, y1, y2)
		switch {
		case s.tail:
			c.set(sx, y0, '▲')
		case s.head:
			c.set(tx, y2, '▼')
		}
	}
	// Labels go on once every line is down, one at a time, so each sees
	// where the ones before it went.
	for _, s := range l.segs {
		if s.label == "" {
			continue
		}
		a, b := l.nodes[s.from], l.nodes[s.to]
		place(c, pickTD(c, s, a.x+a.w/2, b.x+b.w/2, a.y+a.h))
	}
	return c.lines(), true
}

// pickTD chooses where an edge label goes: beside its own arrowhead
// first — which is at the target for an ordinary edge and back up at the
// source for one that closes a cycle — then beside the other end, then
// beside the turn. place drops it if none of them is free.
func pickTD(c *canvas, s seg, sx, tx, y0 int) labelAt {
	far := sx
	if tx > far {
		far = tx
	}
	cands := []labelAt{{tx + 2, y0 + 2, s.label}, {sx + 2, y0, s.label}, {far + 2, y0 + 1, s.label}}
	if s.tail {
		cands[0], cands[1] = cands[1], cands[0]
	}
	for _, at := range cands {
		if c.free(at.x, at.y, runeLen(at.text)+1) {
			return at
		}
	}
	return cands[0]
}

// drawLR lays the ranks out as columns.
func drawLR(l *layered, width int) ([]string, bool) {
	// Every box in a column is widened to the column, so the lines all
	// leave from the same edge.
	colW := make([]int, len(l.ranks))
	for ri, r := range l.ranks {
		for _, v := range r {
			if l.nodes[v].w > colW[ri] {
				colW[ri] = l.nodes[v].w
			}
		}
	}
	// The columns are spaced to fit the edge labels between them; when
	// that does not fit the terminal the space shrinks back to the bare
	// arrow and the labels take their chances (place shortens or drops
	// them) rather than the whole diagram falling back to source.
	wide := minBand
	for _, s := range l.segs {
		if n := runeLen(s.label) + 2 + minBand; s.label != "" && n > wide {
			wide = n
		}
	}
	if wide > 24 {
		wide = 24
	}
	fixed := 0
	for _, w := range colW {
		fixed += w
	}
	band := 0
	for _, try := range []int{wide, minBand} {
		if fixed+try*(len(l.ranks)-1) <= width {
			band = try
			break
		}
	}
	if band == 0 {
		return nil, false
	}
	// Sideways, an invisible node is a single row of line, not a box.
	for i := range l.nodes {
		if l.nodes[i].dummy {
			l.nodes[i].h = 1
		}
	}
	height := 0
	for _, r := range l.ranks {
		h := 0
		for i, v := range r {
			if i > 0 {
				h += gapLR
			}
			h += l.nodes[v].h
		}
		if h > height {
			height = h
		}
	}
	if height > maxLines {
		return nil, false
	}
	x := 0
	for ri, r := range l.ranks {
		h := 0
		for i, v := range r {
			if i > 0 {
				h += gapLR
			}
			h += l.nodes[v].h
		}
		y := (height - h) / 2
		for _, v := range r {
			l.nodes[v].x, l.nodes[v].y = x, y
			l.nodes[v].w = colW[ri]
			y += l.nodes[v].h + gapLR
		}
		x += colW[ri] + band
	}
	c := newCanvas(width, height)
	for _, n := range l.nodes {
		if n.dummy {
			c.hline(n.x, n.x+n.w-1, n.y)
			continue
		}
		c.box(n.x, n.y, n.w, n.h, n.label)
	}
	for _, s := range l.segs {
		a, b := l.nodes[s.from], l.nodes[s.to]
		sy, ty := a.y+a.h/2, b.y+b.h/2
		c0 := a.x + a.w
		c1 := c0 + 1
		end := b.x - 1
		c.hline(c0, c1, sy)
		c.vline(c1, sy, ty)
		c.hline(c1, end, ty)
		switch {
		case s.tail:
			c.set(c0, sy, '◀')
		case s.head:
			c.set(end, ty, '▶')
		}
	}
	for _, s := range l.segs {
		if s.label == "" {
			continue
		}
		a, b := l.nodes[s.from], l.nodes[s.to]
		place(c, pickLR(c, s, a.x+a.w, a.y+a.h/2, b.y+b.h/2))
	}
	return c.lines(), true
}

// pickLR puts a label above the line it belongs to, at the end the
// arrowhead is on for preference.
func pickLR(c *canvas, s seg, c0, sy, ty int) labelAt {
	cands := []labelAt{{c0 + 2, ty - 1, s.label}, {c0 + 2, sy - 1, s.label}, {c0 + 2, sy + 1, s.label}}
	if s.tail {
		cands[0], cands[1] = cands[1], cands[0]
	}
	for _, at := range cands {
		if c.free(at.x, at.y, runeLen(at.text)+1) {
			return at
		}
	}
	return cands[0]
}

// place writes a label into the room it has. A label that does not fit
// is shortened to what is free, and dropped when that is nothing: an
// edge without its label is a small loss, a label written over a line is
// a wrong diagram.
func place(c *canvas, at labelAt) {
	room := 0
	for c.free(at.x+room, at.y, 1) {
		room++
	}
	s := fit(at.text, room-1)
	if s == "" {
		return
	}
	c.text(at.x, at.y, s)
}
