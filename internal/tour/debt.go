package tour

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Debt is the comprehension debt of one package: how many changed lines
// in it were approved — at a prompt, by a rule, or by auto mode — without
// ever being in front of the person in full.
type Debt struct {
	Pkg   string
	Lines int
}

// Meter adds up the debt per package (a path's directory relative to
// root). Each change counts on its own, so lines rewritten twice count
// twice: each was approved once. Mechanical hunks are not debt, nor is an
// undo (the person asked for it) or a change since undone (its lines are
// gone). read reports whether the person saw a change whole.
func Meter(changes []change.Change, read func(change.Change) bool, root string) []Debt {
	scope := NewScope()
	for _, c := range changes {
		scope.Add(c.Path, string(c.Before), string(c.After))
	}
	by := map[string]int{}
	for _, c := range changes {
		if c.Undone || c.Tool == "undo" || read(c) {
			continue
		}
		n := unreadLines(c, scope)
		if n == 0 {
			continue
		}
		pkg := filepath.ToSlash(filepath.Dir(textfmt.RelTo(root, c.Path)))
		by[pkg] += n
	}
	out := make([]Debt, 0, len(by))
	for p, n := range by {
		out = append(out, Debt{Pkg: p, Lines: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Pkg < out[j].Pkg
	})
	return out
}

// unreadLines counts a change's lines in hunks that are not mechanical.
func unreadLines(c change.Change, scope *Scope) int {
	n := 0
	for _, h := range c.Hunks() {
		removed, added := sides(h)
		if Classify(c.Path, removed, added, scope).Mechanical() {
			continue
		}
		n += len(removed) + len(added)
	}
	return n
}

// DebtLine renders the meter in one line, naming at most top packages:
// "comprehension debt: 1200 lines approved unread · 900 in pkg/auth,
// 300 in cmd". "" when there is none.
func DebtLine(d []Debt, top int) string {
	total := sumLines(d)
	if total == 0 {
		return ""
	}
	var parts []string
	for i, x := range d {
		if i == top {
			parts = append(parts, fmt.Sprintf("%d more in %s", sumLines(d[i:]), textfmt.Plural(len(d)-i, "package")))
			break
		}
		parts = append(parts, fmt.Sprintf("%d in %s", x.Lines, x.Pkg))
	}
	return fmt.Sprintf("comprehension debt: %s approved unread · %s", textfmt.Plural(total, "line"), strings.Join(parts, ", "))
}

func sumLines(d []Debt) int {
	n := 0
	for _, x := range d {
		n += x.Lines
	}
	return n
}
