package occ

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/treesitter"
)

// chunk is one change against the base: base lines [start, end) replaced
// by ins. start == end is a pure insertion before base line start.
type chunk struct {
	start, end int
	ins        []string
}

func (c chunk) insertion() bool { return c.start == c.end }

func (c chunk) same(o chunk) bool {
	if c.start != o.start || c.end != o.end || len(c.ins) != len(o.ins) {
		return false
	}
	for i := range c.ins {
		if c.ins[i] != o.ins[i] {
			return false
		}
	}
	return true
}

// chunks groups a line diff of base→v into its changes, in base order.
func chunks(base, v []string) []chunk {
	var out []chunk
	i := 0
	var cur *chunk
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, op := range diff.Lines(base, v) {
		switch op.Kind {
		case ' ':
			flush()
			i++
		case '-':
			if cur == nil {
				cur = &chunk{start: i, end: i}
			}
			i++
			cur.end = i
		case '+':
			if cur == nil {
				cur = &chunk{start: i, end: i}
			}
			cur.ins = append(cur.ins, op.Text)
		}
	}
	flush()
	return out
}

// region is a declaration's span in base lines, 0-based and half-open
// like a chunk, with the names it declares and whether it is a Go import
// block (where two added imports are merged rather than refused).
type region struct {
	start, end int
	names      []string
	imports    bool
}

// regions parses src into its top-level declarations. ok is false when
// there is no parser for the file or it does not parse; the merge then
// works on line ranges alone.
func regions(name string, src []byte) ([]region, bool) {
	if strings.HasSuffix(name, ".go") {
		return goRegions(src)
	}
	spans, ok := treesitter.Spans(name, src)
	if !ok {
		return nil, false
	}
	var names map[int][]string // declaration names by 1-based line
	if p := treesitter.Parser(); p != nil {
		if defs, _, ok := p(name, src); ok {
			names = map[int][]string{}
			for _, d := range defs {
				names[d.Line] = append(names[d.Line], d.Name)
			}
		}
	}
	out := make([]region, 0, len(spans))
	for _, s := range spans {
		r := region{start: s[0] - 1, end: s[1]}
		for line := s[0]; line <= s[1]; line++ {
			r.names = append(r.names, names[line]...)
		}
		out = append(out, r)
	}
	return out, true
}

func goRegions(src []byte) ([]region, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	var out []region
	for _, d := range f.Decls {
		r := region{end: line(d.End())}
		start := d.Pos()
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			r.names = append(r.names, d.Name.Name)
		case *ast.GenDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			r.imports = d.Tok == token.IMPORT
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					r.names = append(r.names, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						r.names = append(r.names, n.Name)
					}
				}
			}
		}
		r.start = line(start) - 1
		out = append(out, r)
	}
	return out, true
}

// covering is the regions a chunk touches. An insertion touches a region
// only when it lands strictly inside it: one between two declarations is
// in neither.
func covering(c chunk, rs []region) []region {
	var out []region
	for _, r := range rs {
		if c.insertion() {
			if r.start < c.start && c.start < r.end {
				out = append(out, r)
			}
		} else if c.start < r.end && r.start < c.end {
			out = append(out, r)
		}
	}
	return out
}

// widen is the chunk's base range grown to the declarations it touches.
func widen(c chunk, rs []region) (int, int) {
	s, e := c.start, c.end
	for _, r := range covering(c, rs) {
		s, e = min(s, r.start), max(e, r.end)
	}
	return s, e
}

// clash reports whether two changes from different sides cannot both be
// applied. With structure, two changes clash when they touch a common
// declaration or their (widened) ranges overlap; an insertion clashes with
// a range it falls strictly inside, and two insertions clash at the same
// point. Without structure, ranges that merely touch clash too, and an
// insertion on a range's edge, as git's own merge treats them: nothing
// says the adjacent lines are independent.
func clash(a, b chunk, rs []region, structured bool) bool {
	as, ae := a.start, a.end
	bs, be := b.start, b.end
	if structured {
		as, ae = widen(a, rs)
		bs, be = widen(b, rs)
	}
	ai, bi := as == ae, bs == be
	switch {
	case ai && bi:
		return as == bs
	case structured && ai:
		// Widened, a range is whole declarations, so an insertion on its
		// edge lands between two of them.
		return bs < as && as < be
	case structured && bi:
		return as < bs && bs < ae
	case ai:
		return bs <= as && as <= be
	case bi:
		return as <= bs && bs <= ae
	case structured:
		return as < be && bs < ae
	default:
		return as <= be && bs <= ae
	}
}

// Merge applies both sides' changes to base when they commute, and
// otherwise says where they do not. A pair of changes that are the same
// edit is applied once.
//
// Two kinds of collision are settled rather than refused, in Go only,
// because they are the commonest overlap two subagents produce and both
// have an order-free answer there: both sides changing the imports (each
// needed a new one) gets one import block holding what both need (see
// importsMerged, then pruneImports), and insertions at one point between
// declarations (each added a function at the end of the file) are both
// kept, ours first. Top-level Go declarations do not depend on their
// order, and the merged file must still parse and declare no name twice,
// or it is refused after all.
func Merge(name string, base, ours, theirs []byte) ([]byte, string) {
	b := diff.SplitLinesKeepEOL(string(base))
	oc := chunks(b, diff.SplitLinesKeepEOL(string(ours)))
	tc := chunks(b, diff.SplitLinesKeepEOL(string(theirs)))
	rs, structured := regions(name, base)
	isGo := structured && strings.HasSuffix(name, ".go")

	var all []chunk
	rewrote := false // the imports were rewritten as one block (see importsMerged)
	if isGo {
		var imp *chunk
		if oc, tc, imp = importsMerged(b, rs, oc, tc, ours, theirs); imp != nil {
			// Anything else either side did that reaches into the
			// rewritten imports is a collision with the rewrite.
			for _, c := range append(append([]chunk{}, oc...), tc...) {
				if clash(c, *imp, rs, structured) {
					return nil, "both sides changed the imports, and one of them more than imports"
				}
			}
			all = append(all, *imp)
			rewrote = true
		}
	}
	skip := make([]bool, len(tc))
	for _, o := range oc {
		for j, t := range tc {
			if skip[j] {
				continue
			}
			if o.same(t) {
				skip[j] = true
				continue
			}
			if !clash(o, t, rs, structured) {
				continue
			}
			if u, ok := union(o, t, rs, isGo); ok {
				o, skip[j] = u, true
				continue
			}
			return nil, describeClash(o, t, b, rs, structured)
		}
		all = append(all, o)
	}
	for j, t := range tc {
		if !skip[j] {
			all = append(all, t)
		}
	}
	// Sorted by position; an insertion before a range at the same start
	// keeps its place ahead of it.
	sortChunks(all)
	var out strings.Builder
	at := 0
	for _, c := range all {
		for ; at < c.start; at++ {
			out.WriteString(b[at])
		}
		for _, l := range c.ins {
			out.WriteString(l)
		}
		at = max(at, c.end)
	}
	for ; at < len(b); at++ {
		out.WriteString(b[at])
	}
	merged := []byte(out.String())
	if rewrote {
		merged = pruneImports(merged)
	}
	if isGo {
		if why := goSound(merged); why != "" {
			return nil, why
		}
	}
	return merged, ""
}

// goSound says what is wrong with a Go file that merged by lines but is
// not a sound file: it does not parse, or it declares a name twice.
func goSound(src []byte) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return "the two sides' changes merge by lines but the result does not parse"
	}
	seen := map[string]bool{}
	dup := func(n string) string {
		if n == "_" || n == "init" {
			return ""
		}
		if seen[n] {
			return "both sides declared " + n
		}
		seen[n] = true
		return ""
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			key := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				key = recvName(d.Recv.List[0].Type) + "." + key
			}
			if why := dup(key); why != "" {
				return why
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if why := dup(s.Name.Name); why != "" {
						return why
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if why := dup(n.Name); why != "" {
							return why
						}
					}
				}
			}
		}
	}
	return ""
}

// recvName is a receiver's type name, pointer or not, without its type
// parameters.
func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return "?"
}

func sortChunks(cs []chunk) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && less(cs[j], cs[j-1]); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

func less(a, b chunk) bool {
	if a.start != b.start {
		return a.start < b.start
	}
	return a.insertion() && !b.insertion()
}

// union merges two pure insertions that collide in a way Go settles
// without asking: both inside one import block, or both at one point
// between declarations. Import lines are not repeated; declarations are
// kept whole, ours first.
func union(a, b chunk, rs []region, isGo bool) (chunk, bool) {
	if !isGo || !a.insertion() || !b.insertion() {
		return chunk{}, false
	}
	inside := func(r region, c chunk) bool { return r.start < c.start && c.start < r.end }
	for _, r := range rs {
		if !r.imports || !inside(r, a) || !inside(r, b) {
			continue
		}
		at := min(a.start, b.start)
		u := chunk{start: at, end: at}
		have := map[string]bool{}
		for _, l := range append(append([]string{}, a.ins...), b.ins...) {
			k := strings.TrimSpace(l)
			if have[k] {
				continue
			}
			have[k] = true
			u.ins = append(u.ins, l)
		}
		return u, true
	}
	if a.start == b.start && len(covering(a, rs)) == 0 && len(covering(b, rs)) == 0 {
		return chunk{start: a.start, end: a.start, ins: append(append([]string{}, a.ins...), b.ins...)}, true
	}
	return chunk{}, false
}

// describeClash is the one line that says where two changes collide.
func describeClash(a, b chunk, base []string, rs []region, structured bool) string {
	if structured {
		var names []string
		seen := map[string]bool{}
		bc := covering(b, rs)
		for _, r := range covering(a, rs) {
			for _, o := range bc {
				if o.start == r.start {
					for _, n := range r.names {
						if !seen[n] {
							seen[n] = true
							names = append(names, n)
						}
					}
				}
			}
		}
		if len(names) > 0 {
			return "both sides changed " + strings.Join(names, ", ")
		}
	}
	lo, hi := min(a.start, b.start)+1, max(a.end, b.end)
	if hi < lo {
		hi = lo
	}
	if lo > len(base) {
		return "both sides added lines at the end of the file"
	}
	return fmt.Sprintf("both sides changed lines %d–%d", lo, hi)
}

// touchedNames is the declarations the change base→theirs touched, on
// either side of it: one it edited or removed is named in base, one it
// added only in theirs. known is false when either side does not parse.
func touchedNames(name string, base, theirs Version) ([]string, bool) {
	if !base.Exists || !theirs.Exists || binary(base.Data) || binary(theirs.Data) {
		return nil, false
	}
	brs, ok := regions(name, base.Data)
	if !ok {
		return nil, false
	}
	trs, ok := regions(name, theirs.Data)
	if !ok {
		return nil, false
	}
	b := diff.SplitLinesKeepEOL(string(base.Data))
	t := diff.SplitLinesKeepEOL(string(theirs.Data))
	seen := map[string]bool{}
	var out []string
	add := func(rs []region) {
		for _, r := range rs {
			for _, n := range r.names {
				if !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
		}
	}
	for _, c := range chunks(b, t) {
		add(covering(c, brs))
	}
	// The same changes seen from theirs: what they added.
	for _, c := range chunks(t, b) {
		add(covering(c, trs))
	}
	return out, true
}
