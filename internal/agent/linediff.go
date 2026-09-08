package agent

import (
	"fmt"
	"strings"
)

// edit is one step of a line diff: '=' kept, '-' removed, '+' added.
type edit struct {
	op   byte
	line string
}

// maxDiffSteps bounds the Myers search; beyond it the diff degrades to
// "replace everything", which is what a rewrite of that size is anyway.
const maxDiffSteps = 3000

// splitLines keeps line endings so the join is exact.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines is a Myers line diff of a against b.
func diffLines(a, b []string) []edit {
	// Trim the common prefix and suffix first: most edits are local.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []edit
	for _, l := range a[:pre] {
		out = append(out, edit{'=', l})
	}
	out = append(out, myers(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, edit{'=', l})
	}
	return out
}

func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	replaceAll := func() []edit {
		out := make([]edit, 0, n+m)
		for _, l := range a {
			out = append(out, edit{'-', l})
		}
		for _, l := range b {
			out = append(out, edit{'+', l})
		}
		return out
	}
	limit := n + m
	if limit > maxDiffSteps {
		limit = maxDiffSteps
	}
	// v[k] is the furthest x on diagonal k; trace keeps v per step for
	// the backtrack, sized to the diagonals in use.
	v := make([]int, 2*limit+3)
	off := limit + 1
	var trace [][]int
	for d := 0; d <= limit; d++ {
		snap := make([]int, 2*d+1)
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace, d)
			}
		}
	}
	return replaceAll()
}

func backtrack(a, b []string, trace [][]int, d int) []edit {
	x, y := len(a), len(b)
	var rev []edit
	for ; d > 0; d-- {
		snap := trace[d]
		at := func(k int) int { return snap[k+d] }
		k := x - y
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, edit{'=', a[x]})
		}
		if x == px {
			y--
			rev = append(rev, edit{'+', b[y]})
		} else {
			x--
			rev = append(rev, edit{'-', a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, edit{'=', a[x]})
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// Hunk is a unified-diff hunk: Lines carry their ' ', '-' or '+' prefix
// and their original line ending.
type Hunk struct {
	OldStart, OldCount, NewStart, NewCount int
	Lines                                  []string
}

const hunkContext = 3

// makeHunks groups a diff into hunks with hunkContext lines of context.
func makeHunks(edits []edit) []Hunk {
	var hunks []Hunk
	oldLine, newLine := 1, 1
	i := 0
	for i < len(edits) {
		if edits[i].op == '=' {
			oldLine++
			newLine++
			i++
			continue
		}
		// Start a hunk hunkContext lines before this change.
		start := i
		lead := 0
		for start > 0 && lead < hunkContext && edits[start-1].op == '=' {
			start--
			lead++
		}
		h := Hunk{OldStart: oldLine - lead, NewStart: newLine - lead}
		j := i
		eq := 0
		for j < len(edits) {
			if edits[j].op == '=' {
				eq++
				if eq > 2*hunkContext {
					break
				}
			} else {
				eq = 0
			}
			j++
		}
		end := j
		if eq > hunkContext {
			end = j - (eq - hunkContext)
		}
		for _, e := range edits[start:end] {
			prefix := " "
			if e.op != '=' {
				prefix = string(e.op)
			}
			h.Lines = append(h.Lines, prefix+e.line)
			if e.op != '+' {
				h.OldCount++
			}
			if e.op != '-' {
				h.NewCount++
			}
		}
		for _, e := range edits[i:end] {
			if e.op != '+' {
				oldLine++
			}
			if e.op != '-' {
				newLine++
			}
		}
		hunks = append(hunks, h)
		i = end
	}
	return hunks
}

// Header is the @@ line of the hunk. OldStart and NewStart are where the
// hunk's lines begin (1-based); an empty side is shown git-style, as the
// line before it.
func (h Hunk) Header() string {
	os, ns := h.OldStart, h.NewStart
	if h.OldCount == 0 {
		os--
	}
	if h.NewCount == 0 {
		ns--
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", os, h.OldCount, ns, h.NewCount)
}

// String renders the hunk as unified diff text.
func (h Hunk) String() string {
	var b strings.Builder
	b.WriteString(h.Header() + "\n")
	for _, l := range h.Lines {
		b.WriteString(l)
		if !strings.HasSuffix(l, "\n") {
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return b.String()
}

// sides splits the hunk into what it looked like before (context and
// removed lines) and after (context and added lines).
func (h Hunk) sides() (old, new []string) {
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			old = append(old, l[1:])
			new = append(new, l[1:])
		case '-':
			old = append(old, l[1:])
		case '+':
			new = append(new, l[1:])
		}
	}
	return old, new
}

// stats counts added and removed lines.
func (h Hunk) stats() (add, del int) {
	for _, l := range h.Lines {
		switch l[0] {
		case '+':
			add++
		case '-':
			del++
		}
	}
	return add, del
}
