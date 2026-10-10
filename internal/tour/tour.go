package tour

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Item is one hunk of the tour: where it is, what kind of change it is,
// why the model said it made it, and how far the file it is in reaches.
type Item struct {
	Path   string
	Hunk   change.Hunk
	Class  Class
	Intent string
	// Refs is how many places use the symbols the file's change touches,
	// from the reach function Build was given; 0 when there was none or
	// it found nothing. It is per file, not per hunk: one analysis of the
	// file's whole change, shared by its hunks.
	Refs int
	// Risk orders the tour, highest first (see risk).
	Risk int
}

// Lines is how many lines the hunk adds and removes.
func (it Item) Lines() int {
	a, d := it.Hunk.Stats()
	return a + d
}

// Tour is one review: the hunks to read, riskiest first, and the
// mechanical ones held back behind a count.
type Tour struct {
	Items      []Item
	Mechanical []Item
	// Seqs are the changes the tour was built from, for a caller that
	// marks them reviewed once the tour has been shown.
	Seqs []int
}

// Reach answers how many places use what changing before into after
// touches, for one file; Build calls it once per file with a behaviour
// hunk. Nil, or a 0, ranks by class and size alone.
type Reach func(path, before, after string) int

// Build is the tour of a set of changes: for each path, the net diff from
// the content before the first change to the content after the last, so
// a hunk edited three times is read once, in its final form. Each hunk
// carries the intent of the latest change whose own diff wrote any of its
// lines.
func Build(changes []change.Change, reach Reach) Tour {
	var t Tour
	var order []string
	byPath := map[string][]change.Change{}
	for _, c := range changes {
		if _, ok := byPath[c.Path]; !ok {
			order = append(order, c.Path)
		}
		byPath[c.Path] = append(byPath[c.Path], c)
		t.Seqs = append(t.Seqs, c.Seq)
	}
	scope := NewScope()
	for _, path := range order {
		before, after := netSides(byPath[path])
		scope.Add(path, before, after)
	}
	for _, path := range order {
		cs := byPath[path]
		before, after := netSides(cs)
		hunks := change.MakeHunks(change.DiffLines(diff.SplitLinesKeepEOL(before), diff.SplitLinesKeepEOL(after)))
		refs, asked := 0, false
		for _, h := range hunks {
			removed, added := sides(h)
			it := Item{Path: path, Hunk: h, Class: Classify(path, removed, added, scope), Intent: intentOf(cs, removed, added)}
			if it.Class == Behaviour && reach != nil {
				if !asked {
					refs, asked = reach(path, before, after), true
				}
				it.Refs = refs
			}
			it.Risk = risk(it)
			if it.Class.Mechanical() {
				t.Mechanical = append(t.Mechanical, it)
			} else {
				t.Items = append(t.Items, it)
			}
		}
	}
	sort.SliceStable(t.Items, func(i, j int) bool { return t.Items[i].Risk > t.Items[j].Risk })
	return t
}

// netSides is a path's content before its first change and after its
// last, "" for a side where there was no file.
func netSides(cs []change.Change) (before, after string) {
	before, after = string(cs[0].Before), string(cs[len(cs)-1].After)
	if !cs[0].Existed {
		before = ""
	}
	if cs[len(cs)-1].Gone {
		after = ""
	}
	return before, after
}

// risk scores one hunk: its class sets the band (a behaviour change
// always outranks a test edit, which always outranks a comment), and
// within a band, reach and then size order it. The band widths are
// larger than anything reach and size can add, so a widely used function
// never pushes a test edit above an unreferenced behaviour change.
func risk(it Item) int {
	band := map[Class]int{Behaviour: 3000, Test: 2000, Comment: 1000}[it.Class]
	return band + min(it.Refs, 50)*10 + min(it.Lines(), 400)
}

// sides is a hunk's removed and added lines, without their prefixes.
func sides(h change.Hunk) (removed, added []string) {
	for _, l := range h.Lines {
		switch l[0] {
		case '-':
			removed = append(removed, l[1:])
		case '+':
			added = append(added, l[1:])
		}
	}
	return removed, added
}

// intentOf is the stated intent behind a net hunk: the latest change on
// the path whose own added lines include one the hunk adds (or, for a
// hunk that only removes, whose removed lines include one it removes).
// Lines that are blank or a lone brace are skipped: every change writes
// those. With no match, the path's last stated intent stands in.
func intentOf(cs []change.Change, removed, added []string) string {
	want, plus := added, true
	if len(nonTrivial(added)) == 0 {
		want, plus = removed, false
	}
	keys := nonTrivial(want)
	for i := len(cs) - 1; i >= 0 && len(keys) > 0; i-- {
		for _, h := range cs[i].Hunks() {
			r, a := sides(h)
			mine := a
			if !plus {
				mine = r
			}
			for _, l := range mine {
				if keys[l] && cs[i].Intent != "" {
					return cs[i].Intent
				}
			}
		}
	}
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Intent != "" {
			return cs[i].Intent
		}
	}
	return ""
}

func nonTrivial(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		if t := strings.TrimSpace(l); len(t) > 1 {
			out[l] = true
		}
	}
	return out
}

// MechanicalLines is how many changed lines the tour collapsed, and how
// many of them each mechanical class accounts for.
func (t Tour) MechanicalLines() (total int, byClass map[Class]int) {
	byClass = map[Class]int{}
	for _, it := range t.Mechanical {
		total += it.Lines()
		byClass[it.Class] += it.Lines()
	}
	return total, byClass
}

// Brief is the tour in one line, for the end of a turn: "" when the
// changes have no hunks at all.
func (t Tour) Brief() string {
	if len(t.Items) == 0 && len(t.Mechanical) == 0 {
		return ""
	}
	counts := map[Class]int{}
	for _, it := range t.Items {
		counts[it.Class]++
	}
	var parts []string
	for _, c := range []Class{Behaviour, Test, Comment} {
		if counts[c] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[c], c))
		}
	}
	line := "review: " + textfmt.Plural(len(t.Items), "hunk")
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	if n, _ := t.MechanicalLines(); n > 0 {
		line += fmt.Sprintf(" · %s mechanical collapsed", textfmt.Plural(n, "line"))
	}
	return line + " · /tour"
}

// Format renders the tour for /tour: a header per hunk (rank, class,
// path relative to root, reach, intent), the hunk itself, and at the end
// the mechanical hunks as one count per class — or, with all, listed in
// full after the rest.
func (t Tour) Format(root string, all bool) string {
	var sb strings.Builder
	for i, it := range t.Items {
		fmt.Fprintf(&sb, "%d. [%s] %s %s", i+1, it.Class, textfmt.RelTo(root, it.Path), it.Hunk.Header())
		if it.Refs > 0 {
			fmt.Fprintf(&sb, " · reaches %s", textfmt.Plural(it.Refs, "use"))
		}
		sb.WriteString("\n")
		if it.Intent != "" {
			sb.WriteString("   intent: " + it.Intent + "\n")
		}
		for _, l := range it.Hunk.Lines {
			sb.WriteString(strings.TrimRight(l, "\r\n") + "\n")
		}
		sb.WriteString("\n")
	}
	if n, by := t.MechanicalLines(); n > 0 {
		var parts []string
		for _, c := range []Class{Rename, Format} {
			if by[c] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", by[c], c))
			}
		}
		fmt.Fprintf(&sb, "%s mechanical collapsed (%s) in %s", textfmt.Plural(n, "line"), strings.Join(parts, ", "), textfmt.Plural(len(t.Mechanical), "hunk"))
		if !all {
			sb.WriteString(" · /tour all shows them")
		}
		sb.WriteString("\n")
		if all {
			sb.WriteString("\n")
			for _, it := range t.Mechanical {
				fmt.Fprintf(&sb, "[%s] %s %s\n", it.Class, textfmt.RelTo(root, it.Path), it.Hunk.Header())
				for _, l := range it.Hunk.Lines {
					sb.WriteString(strings.TrimRight(l, "\r\n") + "\n")
				}
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
