package mermaid

// Pie charts, entity-relationship diagrams and class diagrams. None of
// them needs the layered graph layout: a pie is drawn as proportional
// bars, and the other two as boxes (entities or classes, with their
// members) set in rows, followed by one line per relationship. That keeps
// every relationship readable at any width, and the output is plain text
// lines like every other diagram here.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Slice is one wedge of a pie chart.
type Slice struct {
	Label string
	Value float64
}

// Box is an entity or a class: a title, an optional stereotype, and its
// members in up to two sections (attributes, then methods).
type Box struct {
	Name       string
	Stereotype string
	Attrs      []string
	Methods    []string
}

// Limits for the box diagrams and pies.
const (
	maxBoxes   = 20
	maxMembers = 12
	maxRels    = 40
	maxSlices  = 20
)

// ------------------------------------------------------------------ pie

var pieRowRe = regexp.MustCompile(`^"([^"]+)"\s*:\s*([0-9]+(?:\.[0-9]+)?)$`)

func parsePie(head string, rest []string) (*Diagram, error) {
	d := &Diagram{Kind: Pie}
	h := strings.TrimSpace(strings.TrimPrefix(head, "pie"))
	if strings.HasPrefix(h, "showData") {
		d.ShowData = true
		h = strings.TrimSpace(strings.TrimPrefix(h, "showData"))
	}
	if t, ok := strings.CutPrefix(h, "title "); ok {
		d.Title = clip(t)
	} else if h != "" {
		return nil, errUnsupported
	}
	total := 0.0
	for _, ln := range rest {
		if t, ok := strings.CutPrefix(ln, "title "); ok {
			d.Title = clip(t)
			continue
		}
		m := pieRowRe.FindStringSubmatch(ln)
		if m == nil {
			return nil, errUnsupported
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return nil, errUnsupported
		}
		d.Slices = append(d.Slices, Slice{clip(m[1]), v})
		total += v
		if len(d.Slices) > maxSlices {
			return nil, errTooBig
		}
	}
	if len(d.Slices) == 0 || total <= 0 {
		return nil, errUnsupported
	}
	return d, nil
}

func drawPie(d *Diagram, width int) ([]string, bool) {
	total := 0.0
	labelW, valW := 0, 0
	for _, s := range d.Slices {
		total += s.Value
		labelW = max(labelW, runeLen(s.Label))
	}
	vals := make([]string, len(d.Slices))
	for i, s := range d.Slices {
		pct := fmt.Sprintf("%.1f%%", s.Value/total*100)
		if d.ShowData {
			vals[i] = fmt.Sprintf("%s (%s)", strconv.FormatFloat(s.Value, 'f', -1, 64), pct)
		} else {
			vals[i] = pct
		}
		valW = max(valW, runeLen(vals[i]))
	}
	bar := min(40, width-labelW-valW-4)
	if bar < 8 {
		return nil, false
	}
	var out []string
	if d.Title != "" {
		out = append(out, " "+d.Title, "")
	}
	for i, s := range d.Slices {
		n := int(s.Value/total*float64(bar) + 0.5)
		if n == 0 && s.Value > 0 {
			n = 1
		}
		out = append(out, " "+padRight(s.Label, labelW)+" "+padRight(strings.Repeat("█", n), bar)+" "+vals[i])
	}
	for i := range out {
		out[i] = strings.TrimRight(out[i], " ")
	}
	return out, len(out) <= maxLines
}

func padRight(s string, n int) string {
	if k := runeLen(s); k < n {
		return s + strings.Repeat(" ", n-k)
	}
	return s
}

// ------------------------------------------------------------------- ER

var (
	erRelRe  = regexp.MustCompile(`^([A-Za-z_][\w\-]*)\s+(\|\||\|o|\}o|\}\|)(--|\.\.)(\|\||o\||o\{|\|\{)\s+([A-Za-z_][\w\-]*)\s*(?::\s*(.*))?$`)
	erOpenRe = regexp.MustCompile(`^([A-Za-z_][\w\-]*)\s*\{$`)
	erNameRe = regexp.MustCompile(`^[A-Za-z_][\w\-]*$`)
	erAttrRe = regexp.MustCompile(`^([\w\-\(\),\[\]]+)\s+([\w\-]+)((?:\s+(?:PK|FK|UK)(?:\s*,\s*(?:PK|FK|UK))*)*)(?:\s+"([^"]*)")?$`)
)

var erCard = map[string]string{
	"||": "1", "|o": "0..1", "o|": "0..1", "}o": "0..*", "o{": "0..*", "}|": "1..*", "|{": "1..*",
}

func parseER(rest []string) (*Diagram, error) {
	d := &Diagram{Kind: ER}
	idx := map[string]int{}
	ent := func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		idx[name] = len(d.Boxes)
		d.Boxes = append(d.Boxes, Box{Name: clip(name)})
		return idx[name]
	}
	cur := -1
	for _, ln := range rest {
		if cur >= 0 {
			if ln == "}" {
				cur = -1
				continue
			}
			m := erAttrRe.FindStringSubmatch(ln)
			if m == nil {
				return nil, errUnsupported
			}
			a := m[1] + " " + m[2]
			if k := strings.Join(strings.Fields(strings.ReplaceAll(m[3], ",", " ")), ","); k != "" {
				a += " " + k
			}
			if m[4] != "" {
				a += " \"" + m[4] + "\""
			}
			if len(d.Boxes[cur].Attrs) >= maxMembers {
				return nil, errTooBig
			}
			d.Boxes[cur].Attrs = append(d.Boxes[cur].Attrs, clip(a))
			continue
		}
		if m := erOpenRe.FindStringSubmatch(ln); m != nil {
			cur = ent(m[1])
			continue
		}
		if m := erRelRe.FindStringSubmatch(ln); m != nil {
			ent(m[1])
			ent(m[5])
			line := "-"
			if m[3] == ".." {
				line = "╌"
			}
			conn := strings.Repeat(map[string]string{"-": "─", "╌": "╌"}[line], 4)
			s := fmt.Sprintf("%s %s %s %s %s", clip(m[1]), erCard[m[2]], conn, erCard[m[4]], clip(m[5]))
			if l := strings.Trim(strings.TrimSpace(m[6]), `"`); l != "" {
				s += " : " + clip(l)
			}
			d.Rels = append(d.Rels, s)
			if len(d.Rels) > maxRels {
				return nil, errTooBig
			}
			continue
		}
		if erNameRe.MatchString(ln) {
			ent(ln)
			continue
		}
		return nil, errUnsupported
	}
	if cur >= 0 || len(d.Boxes) == 0 {
		return nil, errUnsupported
	}
	if len(d.Boxes) > maxBoxes {
		return nil, errTooBig
	}
	return d, nil
}

// ---------------------------------------------------------------- class

var (
	clsHeadRe  = regexp.MustCompile(`^class\s+([A-Za-z_][\w\-]*)\s*(\{)?$`)
	clsMemberR = regexp.MustCompile(`^([A-Za-z_][\w\-]*)\s*:\s*(.+)$`)
	clsRelRe   = regexp.MustCompile(`^([A-Za-z_][\w\-]*)\s*(?:"([^"]*)")?\s*(<\|--|--\|>|\*--|--\*|o--|--o|<\|\.\.|\.\.\|>|-->|<--|\.\.>|<\.\.|--|\.\.)\s*(?:"([^"]*)")?\s*([A-Za-z_][\w\-]*)\s*(?::\s*(.*))?$`)
	clsStereo  = regexp.MustCompile(`^<<([^<>]+)>>$`)
)

// clsGlyph turns a relationship operator into the arrow drawn for it.
var clsGlyph = map[string]string{
	"<|--": "◁───", "--|>": "───▷", "*--": "◆───", "--*": "───◆", "o--": "◇───", "--o": "───◇",
	"-->": "───▶", "<--": "◀───", "--": "────",
	"..>": "╌╌╌▶", "<..": "◀╌╌╌", "..|>": "╌╌╌▷", "<|..": "◁╌╌╌", "..": "╌╌╌╌",
}

func parseClass(rest []string) (*Diagram, error) {
	d := &Diagram{Kind: Class}
	idx := map[string]int{}
	cls := func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		idx[name] = len(d.Boxes)
		d.Boxes = append(d.Boxes, Box{Name: clip(name)})
		return idx[name]
	}
	member := func(i int, text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		if m := clsStereo.FindStringSubmatch(text); m != nil {
			d.Boxes[i].Stereotype = clip(m[1])
			return nil
		}
		// Generics (`List~int~`), static/abstract suffixes and the like
		// are not drawn faithfully, so they are refused.
		if strings.Contains(text, "~") {
			return errUnsupported
		}
		b := &d.Boxes[i]
		if len(b.Attrs)+len(b.Methods) >= maxMembers {
			return errTooBig
		}
		if strings.Contains(text, "(") {
			b.Methods = append(b.Methods, clip(text))
		} else {
			b.Attrs = append(b.Attrs, clip(text))
		}
		return nil
	}
	cur := -1
	for _, ln := range rest {
		if cur >= 0 {
			if ln == "}" {
				cur = -1
				continue
			}
			if err := member(cur, ln); err != nil {
				return nil, err
			}
			continue
		}
		switch {
		case strings.HasPrefix(ln, "direction "):
			continue // placement is ours
		}
		if m := clsHeadRe.FindStringSubmatch(ln); m != nil {
			i := cls(m[1])
			if m[2] == "{" {
				cur = i
			}
			continue
		}
		if m := clsRelRe.FindStringSubmatch(ln); m != nil {
			cls(m[1])
			cls(m[5])
			s := clip(m[1])
			if m[2] != "" {
				s += ` "` + m[2] + `"`
			}
			s += " " + clsGlyph[m[3]]
			if m[4] != "" {
				s += ` "` + m[4] + `"`
			}
			s += " " + clip(m[5])
			if l := strings.TrimSpace(m[6]); l != "" {
				s += " : " + clip(l)
			}
			d.Rels = append(d.Rels, s)
			if len(d.Rels) > maxRels {
				return nil, errTooBig
			}
			continue
		}
		if m := clsMemberR.FindStringSubmatch(ln); m != nil {
			if err := member(cls(m[1]), m[2]); err != nil {
				return nil, err
			}
			continue
		}
		return nil, errUnsupported
	}
	if cur >= 0 || len(d.Boxes) == 0 {
		return nil, errUnsupported
	}
	if len(d.Boxes) > maxBoxes {
		return nil, errTooBig
	}
	return d, nil
}

// ---------------------------------------------------------- drawing boxes

// boxLines draws one box as equal-width rows.
func boxLines(b Box) []string {
	title := b.Name
	w := runeLen(title)
	if b.Stereotype != "" {
		w = max(w, runeLen("<<"+b.Stereotype+">>"))
	}
	for _, s := range b.Attrs {
		w = max(w, runeLen(s))
	}
	for _, s := range b.Methods {
		w = max(w, runeLen(s))
	}
	w += 2 // a space either side
	center := func(s string) string {
		pad := w - runeLen(s)
		return "│" + strings.Repeat(" ", pad/2) + s + strings.Repeat(" ", pad-pad/2) + "│"
	}
	left := func(s string) string { return "│ " + padRight(s, w-1) + "│" }
	bar := func(l, r string) string { return l + strings.Repeat("─", w) + r }
	out := []string{bar("┌", "┐")}
	if b.Stereotype != "" {
		out = append(out, center("<<"+b.Stereotype+">>"))
	}
	out = append(out, center(title))
	for _, sec := range [][]string{b.Attrs, b.Methods} {
		if len(sec) == 0 {
			continue
		}
		out = append(out, bar("├", "┤"))
		for _, s := range sec {
			out = append(out, left(s))
		}
	}
	return append(out, bar("└", "┘"))
}

// drawBoxes sets the boxes in rows no wider than width, then lists the
// relationships.
func drawBoxes(d *Diagram, width int) ([]string, bool) {
	const gap = 2
	var out []string
	var row [][]string
	rowW := 0
	flush := func() {
		if len(row) == 0 {
			return
		}
		h := 0
		for _, b := range row {
			h = max(h, len(b))
		}
		for y := 0; y < h; y++ {
			var sb strings.Builder
			for i, b := range row {
				if i > 0 {
					sb.WriteString(strings.Repeat(" ", gap))
				}
				if y < len(b) {
					sb.WriteString(b[y])
				} else {
					sb.WriteString(strings.Repeat(" ", runeLen(b[0])))
				}
			}
			out = append(out, strings.TrimRight(sb.String(), " "))
		}
		out = append(out, "")
		row, rowW = nil, 0
	}
	for _, b := range d.Boxes {
		bl := boxLines(b)
		bw := runeLen(bl[0])
		if bw > width {
			return nil, false
		}
		if rowW > 0 && rowW+gap+bw > width {
			flush()
		}
		if rowW > 0 {
			rowW += gap
		}
		row = append(row, bl)
		rowW += bw
	}
	flush()
	for _, r := range d.Rels {
		if runeLen(r) > width {
			return nil, false
		}
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out, len(out) <= maxLines
}
