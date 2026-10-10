package occ

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// importLine is a line that can only be part of an import declaration:
// the keyword with or without a block, a closing paren, or a spec.
var importLine = regexp.MustCompile(`^(import\s*\(|\)|import\s+([\w.]+\s+)?"[^"]*"|([\w.]+\s+)?"[^"]*"|)$`)

// importsMerged settles the overlap two subagents produce most often in
// Go: both changed the imports, each to add what its own code needs. The
// common shape, a single `import "fmt"` that each side turned into a
// block, is one line replaced by two different blocks, which no line
// merge can take. Both sides' imports are taken instead — the base's,
// less any either side removed, plus whatever either added — and written
// as one block in place of the base's import declarations. The chunks
// that made up the two import changes are dropped from oc and tc and the
// replacement returned.
//
// It declines, leaving the chunks to the ordinary merge, when only one
// side touched the imports, when a comment sits in or near them (it
// would be lost), or when a side does not parse.
func importsMerged(base []string, rs []region, oc, tc []chunk, ours, theirs []byte) ([]chunk, []chunk, *chunk) {
	var imps []region
	for _, r := range rs {
		if r.imports {
			imps = append(imps, r)
		}
	}
	if len(imps) == 0 {
		return oc, tc, nil
	}
	lo, hi := imps[0].start, imps[len(imps)-1].end
	inImports := func(c chunk) bool {
		if c.start < lo || c.end > hi {
			return false
		}
		for _, l := range c.ins {
			if !importLine.MatchString(strings.TrimSpace(l)) {
				return false
			}
		}
		if c.insertion() {
			return true // an import line inserted in or next to the imports
		}
		for _, r := range covering(c, rs) {
			if !r.imports {
				return false
			}
		}
		return true
	}
	split := func(cs []chunk) (in, out []chunk) {
		for _, c := range cs {
			if inImports(c) {
				in = append(in, c)
			} else {
				out = append(out, c)
			}
		}
		return in, out
	}
	oi, oo := split(oc)
	ti, to := split(tc)
	if len(oi) == 0 || len(ti) == 0 {
		return oc, tc, nil
	}
	for _, l := range base[lo:hi] {
		if strings.Contains(l, "//") || strings.Contains(l, "/*") {
			return oc, tc, nil
		}
	}
	bset, oset, tset := importSet([]byte(strings.Join(base, ""))), importSet(ours), importSet(theirs)
	if bset == nil || oset == nil || tset == nil {
		return oc, tc, nil
	}
	keep := map[string]bool{}
	for k := range bset {
		if oset[k] && tset[k] {
			keep[k] = true
		}
	}
	for _, side := range []map[string]bool{oset, tset} {
		for k := range side {
			if !bset[k] {
				keep[k] = true
			}
		}
	}
	specs := make([]string, 0, len(keep))
	for k := range keep {
		specs = append(specs, k)
	}
	// gofmt's order within one group: by path.
	sort.Slice(specs, func(i, j int) bool { return specPath(specs[i]) < specPath(specs[j]) })
	eol := "\n"
	if strings.HasSuffix(base[lo], "\r\n") {
		eol = "\r\n"
	}
	c := chunk{start: lo, end: hi}
	if len(specs) > 0 {
		c.ins = append(c.ins, "import ("+eol)
		for _, s := range specs {
			c.ins = append(c.ins, "\t"+s+eol)
		}
		c.ins = append(c.ins, ")"+eol)
	}
	return oo, to, &c
}

// importSet is the file's import specs as written (`name "path"` or
// `"path"`), or nil when it does not parse.
func importSet(src []byte) map[string]bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, s := range f.Imports {
		k := s.Path.Value
		if s.Name != nil {
			k = s.Name.Name + " " + k
		}
		out[k] = true
	}
	return out
}

func specPath(spec string) string {
	if i := strings.LastIndex(spec, " "); i >= 0 {
		return spec[i+1:]
	}
	return spec
}

// pruneImports drops the imports the merged file no longer uses. Taking
// the union of both sides' imports keeps one a side removed along with
// its last use, and Go refuses an unused import as firmly as a missing
// one. An import whose package name cannot be told from its path is
// kept; so are blank, dot and cgo imports.
func pruneImports(src []byte) []byte {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return src
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	drop := map[int]bool{}
	for _, s := range f.Imports {
		name := ""
		if s.Name != nil {
			name = s.Name.Name
		} else {
			name = pkgName(strings.Trim(s.Path.Value, `"`))
		}
		if name == "" || name == "_" || name == "." || name == "C" || used[name] {
			continue
		}
		start, end := fset.Position(s.Pos()).Line, fset.Position(s.End()).Line
		if start != end {
			continue
		}
		drop[start] = true
	}
	if len(drop) == 0 {
		return src
	}
	var b strings.Builder
	for i, l := range strings.SplitAfter(string(src), "\n") {
		// A single-line `import "x"` is a whole declaration: dropping the
		// line drops it. A spec in a block is one line of the block.
		if !drop[i+1] {
			b.WriteString(l)
		}
	}
	return []byte(b.String())
}

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pkgName is the package name an import path conventionally declares,
// or "" when the path does not say (a dotted or dashed last element).
func pkgName(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if regexp.MustCompile(`^v[0-9]+$`).MatchString(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	if !plainIdent.MatchString(last) {
		return ""
	}
	return last
}
