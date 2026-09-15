// Package change is the unit of provenance: one file changed by one tool
// call, with the content around the call, and the line-level diff of it.
// It is data plus the arithmetic over that data — no agent, no session —
// so both internal/agent, which records changes, and internal/undoabout,
// which decides which of them to revert, can work on the same type.
package change

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
)

// Edit is one step of a line diff: Op is '=' kept, '-' removed, '+'
// added, and Line is the line itself.
type Edit struct {
	Op   byte
	Line string
}

// DiffLines is a thin adapter over diff.Lines, the shared Myers line-diff
// implementation: it just relabels diff.Op's ' ' (equal) as '=' to match
// this package's Edit type.
func DiffLines(a, b []string) []Edit {
	ops := diff.Lines(a, b)
	out := make([]Edit, len(ops))
	for i, op := range ops {
		k := op.Kind
		if k == ' ' {
			k = '='
		}
		out[i] = Edit{Op: k, Line: op.Text}
	}
	return out
}

// Hunk is a unified-diff hunk: Lines carry their ' ', '-' or '+' prefix
// and their original line ending.
type Hunk struct {
	OldStart, OldCount, NewStart, NewCount int
	Lines                                  []string
}

const hunkContext = 3

// MakeHunks groups a diff into hunks with hunkContext lines of context.
func MakeHunks(edits []Edit) []Hunk {
	var hunks []Hunk
	oldLine, newLine := 1, 1
	i := 0
	for i < len(edits) {
		if edits[i].Op == '=' {
			oldLine++
			newLine++
			i++
			continue
		}
		// Start a hunk hunkContext lines before this change.
		start := i
		lead := 0
		for start > 0 && lead < hunkContext && edits[start-1].Op == '=' {
			start--
			lead++
		}
		h := Hunk{OldStart: oldLine - lead, NewStart: newLine - lead}
		j := i
		eq := 0
		for j < len(edits) {
			if edits[j].Op == '=' {
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
			if e.Op != '=' {
				prefix = string(e.Op)
			}
			h.Lines = append(h.Lines, prefix+e.Line)
			if e.Op != '+' {
				h.OldCount++
			}
			if e.Op != '-' {
				h.NewCount++
			}
		}
		for _, e := range edits[i:end] {
			if e.Op != '+' {
				oldLine++
			}
			if e.Op != '-' {
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
// Sides are the hunk's old and new line blocks.
func (h Hunk) Sides() (old, new []string) {
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
// Stats counts the lines the hunk adds and removes.
func (h Hunk) Stats() (add, del int) {
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
