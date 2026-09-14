// Package diff produces unified diffs for showing users what an edit does.
package diff

import (
	"fmt"
	"strings"
)

// Op is one line of a diff.
type Op struct {
	Kind byte // ' ' equal, '-' removed, '+' added
	Text string
}

// Lines computes a line-level diff of a and b using the classic O(ND)
// algorithm restricted with a prefix/suffix trim, which keeps typical edits
// (a few changed lines in a large file) fast.
func Lines(a, b []string) []Op {
	// Trim common prefix and suffix.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mid := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	ops := make([]Op, 0, pre+len(mid)+suf)
	for _, l := range a[:pre] {
		ops = append(ops, Op{' ', l})
	}
	ops = append(ops, mid...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, Op{' ', l})
	}
	return ops
}

// myers is the standard shortest-edit-script search with a size guard: for
// very large or dissimilar inputs it falls back to delete-all/insert-all.
func myers(a, b []string) []Op {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	const maxD = 4000
	max := n + m
	if max > maxD*2 {
		return replaceAll(a, b)
	}
	offset := max
	v := make([]int, 2*max+2)
	var trace [][]int
	for d := 0; d <= max; d++ {
		if d > maxD {
			return replaceAll(a, b)
		}
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, offset)
			}
		}
	}
	return replaceAll(a, b)
}

func backtrack(trace [][]int, a, b []string, offset int) []Op {
	x, y := len(a), len(b)
	var rev []Op
	for d := len(trace) - 1; d >= 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, Op{' ', a[x]})
		}
		if d > 0 {
			if x == prevX {
				y--
				rev = append(rev, Op{'+', b[y]})
			} else {
				x--
				rev = append(rev, Op{'-', a[x]})
			}
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func replaceAll(a, b []string) []Op {
	ops := make([]Op, 0, len(a)+len(b))
	for _, l := range a {
		ops = append(ops, Op{'-', l})
	}
	for _, l := range b {
		ops = append(ops, Op{'+', l})
	}
	return ops
}

// Unified renders ops as a unified diff with the given context lines. The
// header names come from the caller. Returns "" when there are no changes.
func Unified(name string, before, after string, context int) string {
	a := splitLines(before)
	b := splitLines(after)
	ops := Lines(a, b)
	changed := false
	for _, op := range ops {
		if op.Kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", name, name)
	// Build hunks.
	type hunk struct {
		start, end int // op index range [start,end)
	}
	var hunks []hunk
	last := -1
	for i, op := range ops {
		if op.Kind == ' ' {
			continue
		}
		if last >= 0 && i-hunks[last].end <= 2*context {
			hunks[last].end = i + 1
		} else {
			hunks = append(hunks, hunk{i, i + 1})
			last++
		}
	}
	aLine, bLine := 1, 1
	opIdx := 0
	advance := func(to int) {
		for ; opIdx < to; opIdx++ {
			switch ops[opIdx].Kind {
			case ' ':
				aLine++
				bLine++
			case '-':
				aLine++
			case '+':
				bLine++
			}
		}
	}
	for _, h := range hunks {
		start := h.start - context
		if start < 0 {
			start = 0
		}
		end := h.end + context
		if end > len(ops) {
			end = len(ops)
		}
		advance(start)
		aStart, bStart := aLine, bLine
		aCount, bCount := 0, 0
		var body strings.Builder
		for _, op := range ops[start:end] {
			body.WriteByte(op.Kind)
			body.WriteString(op.Text)
			body.WriteByte('\n')
			switch op.Kind {
			case ' ':
				aCount++
				bCount++
			case '-':
				aCount++
			case '+':
				bCount++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		sb.WriteString(body.String())
		advance(end)
	}
	return sb.String()
}

// Stats counts added and removed lines.
func Stats(before, after string) (added, removed int) {
	for _, op := range Lines(splitLines(before), splitLines(after)) {
		switch op.Kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return
}

// SplitLinesKeepEOL splits s into lines that keep their "\n", so joining
// them gives s back exactly; a final newline does not produce a trailing
// empty line. It is the splitter for callers that rebuild or compare file
// content line by line (internal/agent's change tracking, internal/proof's
// landed-change check).
func SplitLinesKeepEOL(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// splitLines is SplitLinesKeepEOL without the line endings.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
