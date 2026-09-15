package mermaid

import (
	"strings"
	"unicode/utf8"
)

// canvas is a fixed grid the layouts draw into. A cell holds either a
// character (a box border, an arrowhead, a letter) or a set of the four
// directions a line leaves it by; the directions are resolved to a box
// character at the end, so two lines meeting come out as the junction
// they are instead of one rubbing the other out. Everything is written
// by absolute position, so a layout is a pure function of the graph and
// the width: same input, same cells, every time.
type canvas struct {
	w, h  int
	cells []rune
	mask  []uint8
}

// The directions a line leaves a cell by.
const (
	up uint8 = 1 << iota
	down
	left
	right
)

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, cells: make([]rune, w*h), mask: make([]uint8, w*h)}
	for i := range c.cells {
		c.cells[i] = ' '
	}
	return c
}

func (c *canvas) in(x, y int) bool { return x >= 0 && y >= 0 && x < c.w && y < c.h }

func (c *canvas) set(x, y int, r rune) {
	if c.in(x, y) {
		c.cells[y*c.w+x] = r
		c.mask[y*c.w+x] = 0
	}
}

// stroke adds the directions a line leaves (x, y) by. A cell that
// already holds a character — a box border, an arrowhead — keeps it.
func (c *canvas) stroke(x, y int, dirs uint8) {
	if !c.in(x, y) || c.cells[y*c.w+x] != ' ' {
		return
	}
	c.mask[y*c.w+x] |= dirs
}

// hline strokes a horizontal run between two columns on the same row,
// inclusive of both ends.
func (c *canvas) hline(x1, x2, y int) {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	for x := x1; x <= x2; x++ {
		var d uint8
		if x > x1 {
			d |= left
		}
		if x < x2 {
			d |= right
		}
		c.stroke(x, y, d)
	}
}

// vline strokes a vertical run between two rows in the same column.
func (c *canvas) vline(x, y1, y2 int) {
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	for y := y1; y <= y2; y++ {
		var d uint8
		if y > y1 {
			d |= up
		}
		if y < y2 {
			d |= down
		}
		c.stroke(x, y, d)
	}
}

// free reports whether n cells from (x, y) are untouched and on canvas.
func (c *canvas) free(x, y, n int) bool {
	if x < 0 || y < 0 || y >= c.h || x+n > c.w {
		return false
	}
	for i := 0; i < n; i++ {
		if c.cells[y*c.w+x+i] != ' ' || c.mask[y*c.w+x+i] != 0 {
			return false
		}
	}
	return true
}

// text writes a string from (x, y), left to right, clipped at the edge.
func (c *canvas) text(x, y int, s string) {
	for _, r := range s {
		c.set(x, y, r)
		x++
	}
}

// box draws a w×h frame with the label centred inside it.
func (c *canvas) box(x, y, w, h int, label string) {
	c.set(x, y, '┌')
	c.set(x+w-1, y, '┐')
	c.set(x, y+h-1, '└')
	c.set(x+w-1, y+h-1, '┘')
	for i := 1; i < w-1; i++ {
		c.set(x+i, y, '─')
		c.set(x+i, y+h-1, '─')
	}
	for j := 1; j < h-1; j++ {
		c.set(x, y+j, '│')
		c.set(x+w-1, y+j, '│')
		for i := 1; i < w-1; i++ {
			c.set(x+i, y+j, ' ')
		}
	}
	pad := (w - 2 - runeLen(label)) / 2
	if pad < 0 {
		pad = 0
	}
	c.text(x+1+pad, y+h/2, label)
}

// junction is the character for every set of directions.
var junction = map[uint8]rune{
	up: '│', down: '│', up | down: '│',
	left: '─', right: '─', left | right: '─',
	up | right: '└', up | left: '┘', down | right: '┌', down | left: '┐',
	up | down | left: '┤', up | down | right: '├',
	up | left | right: '┴', down | left | right: '┬',
	up | down | left | right: '┼',
}

// lines renders the canvas, with the trailing blanks of each row cut and
// the blank rows at top and bottom dropped.
func (c *canvas) lines() []string {
	out := make([]string, 0, c.h)
	var row strings.Builder
	for y := 0; y < c.h; y++ {
		row.Reset()
		for x := 0; x < c.w; x++ {
			i := y*c.w + x
			if m := c.mask[i]; c.cells[i] == ' ' && m != 0 {
				row.WriteRune(junction[m])
				continue
			}
			row.WriteRune(c.cells[i])
		}
		out = append(out, strings.TrimRight(row.String(), " "))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// fit shortens s to n columns, marking the cut, and returns "" when
// there is not even room for that.
func fit(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	if n < 2 {
		return ""
	}
	return string([]rune(s)[:n-1]) + "…"
}
