package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
)

// edit is one step of a line diff: '=' kept, '-' removed, '+' added.
type edit struct {
	op   byte
	line string
}

// diffLines is a thin adapter over diff.Lines, the shared Myers line-diff
// implementation: it just relabels diff.Op's ' ' (equal) as '=' to match
// this package's edit type.
func diffLines(a, b []string) []edit {
	ops := diff.Lines(a, b)
	out := make([]edit, len(ops))
	for i, op := range ops {
		k := op.Kind
		if k == ' ' {
			k = '='
		}
		out[i] = edit{op: k, line: op.Text}
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
