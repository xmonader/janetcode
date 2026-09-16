// Package repomap ranks a repository's files for a request and renders a
// compact map of them: which files matter, and the symbols they define.
// Files are nodes, "A uses a symbol defined in B" is an edge, and a
// PageRank personalised by the request (paths, words and symbols it
// mentions, plus files that changed recently) orders them. Go is parsed
// with go/parser; other languages use regular expressions.
package repomap

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/gitx"
)

// Symbol is a top-level definition.
type Symbol struct {
	Name string
	Kind string // func, method, type, var, const, class, ...
	Line int
	// Refs counts references from other files (filled by Build).
	Refs int
}

// File is one indexed source file.
type File struct {
	Path string // relative, slash-separated
	Defs []Symbol
	refs map[string]int // identifiers used, with counts
	size int
}

// Index is the graph over a repository's source files.
type Index struct {
	Root        string
	Fingerprint string
	Files       []*File
	sym         Parser
	byPath      map[string]int
	definers    map[string][]int // symbol name → files defining it
	out         [][]edge         // file → files it depends on
	in          [][]edge         // file → files depending on it
}

type edge struct {
	to     int
	weight float64
}

// Limits keep large repositories cheap to index.
const (
	MaxFiles     = 5000
	MaxFileBytes = 256 * 1024
	// maxDefiners: a symbol defined in more files than this is too
	// generic to carry an edge (Error, String, New).
	maxDefiners = 3
)

var sourceExt = map[string]bool{".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".rs": true, ".java": true, ".kt": true, ".rb": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".swift": true, ".zig": true, ".php": true, ".scala": true, ".ex": true, ".exs": true}

// Build indexes the repository at root. sym, when not nil, parses the
// languages it covers with a grammar instead of the built-in patterns.
func Build(ctx context.Context, root string, sym Parser) (*Index, error) {
	paths, err := listFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	ix := &Index{Root: root, Fingerprint: gitx.TreeFingerprint(root), sym: sym, byPath: map[string]int{}, definers: map[string][]int{}}
	for _, p := range paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		st, err := os.Stat(abs)
		if err != nil || st.Size() > MaxFileBytes {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		f := parse(p, data, sym)
		ix.byPath[p] = len(ix.Files)
		ix.Files = append(ix.Files, f)
		for _, d := range f.Defs {
			ix.definers[d.Name] = append(ix.definers[d.Name], len(ix.Files)-1)
		}
		if len(ix.Files) >= MaxFiles {
			break
		}
	}
	ix.link()
	return ix, nil
}

// listFiles asks git for tracked and untracked (not ignored) files, and
// walks the tree when git is unavailable.
func listFiles(ctx context.Context, root string) ([]string, error) {
	var out []string
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	if raw, err := cmd.Output(); err == nil {
		for _, p := range strings.Split(string(raw), "\x00") {
			if p != "" && sourceExt[filepath.Ext(p)] {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" || name == "dist" || name == "build") {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExt[filepath.Ext(name)] {
			if rel, err := filepath.Rel(root, p); err == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

var (
	reIdent = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	// Definition patterns per language family; group 1 is the name.
	rePython = regexp.MustCompile(`(?m)^\s*(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)
	reJS     = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function\*?|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)`)
	reRust   = regexp.MustCompile(`(?m)^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?(?:fn|struct|enum|trait|type|mod|const|static)\s+([A-Za-z_]\w*)`)
	reJava   = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|static|final|abstract|sealed|open|data|override|internal)\s+)*(?:class|interface|enum|record|object|fun|def)\s+([A-Za-z_]\w*)`)
	reRuby   = regexp.MustCompile(`(?m)^\s*(?:def|class|module)\s+(?:self\.)?([A-Za-z_]\w*[?!]?)`)
	reC      = regexp.MustCompile(`(?m)^(?:[A-Za-z_][\w:<>,\s\*&]*?\s+\**)?([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:const\s*)?\{`)
	reCType  = regexp.MustCompile(`(?m)^\s*(?:typedef\s+)?(?:struct|enum|union|class)\s+([A-Za-z_]\w*)`)
)

// A Parser extracts a file's definitions and the identifiers it uses from
// a real grammar rather than from the regular expressions below. The
// tree-sitter build (internal/treesitter, behind a build tag) supplies
// one and the composition root hands it to the index; without it the
// built-in parsers are used. It returns false for a file it cannot parse,
// and the caller falls back.
//
// Go is never handed to it: the standard library's own parser is more
// accurate than any grammar for Go, and it is always compiled in.
type Parser func(path string, src []byte) (defs []Symbol, refs []string, ok bool)

// parse extracts definitions and identifier uses from one file.
func parse(path string, data []byte, sym Parser) *File {
	f := &File{Path: path, refs: map[string]int{}, size: len(data)}
	if strings.HasSuffix(path, ".go") {
		if parseGo(f, data) {
			return f
		}
	}
	if sym != nil {
		if defs, refs, ok := sym(path, data); ok {
			f.Defs = dedupe(defs)
			for _, id := range refs {
				if len(id) > 2 {
					f.refs[id]++
				}
			}
			return f
		}
	}
	src := string(data)
	var defs []Symbol
	add := func(re *regexp.Regexp, kind string) {
		for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
			name := src[m[2]:m[3]]
			defs = append(defs, Symbol{Name: name, Kind: kind, Line: 1 + strings.Count(src[:m[2]], "\n")})
		}
	}
	switch filepath.Ext(path) {
	case ".py":
		add(rePython, "def")
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		add(reJS, "def")
	case ".rs":
		add(reRust, "def")
	case ".java", ".kt", ".scala", ".cs", ".swift":
		add(reJava, "def")
	case ".rb", ".ex", ".exs":
		add(reRuby, "def")
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".zig", ".php":
		add(reC, "func")
		add(reCType, "type")
	default:
		add(reJS, "def")
	}
	f.Defs = dedupe(defs)
	for _, id := range reIdent.FindAllString(src, -1) {
		f.refs[id]++
	}
	return f
}

// parseGo uses the real parser; false means a syntax error, so the
// caller falls back to the regular expressions.
func parseGo(f *File, data []byte) bool {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f.Path, data, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, decl := range af.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name, kind := d.Name.Name, "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				name = recvName(d.Recv.List[0].Type) + "." + name
			}
			f.Defs = append(f.Defs, Symbol{Name: name, Kind: kind, Line: fset.Position(d.Pos()).Line})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					f.Defs = append(f.Defs, Symbol{Name: s.Name.Name, Kind: "type", Line: fset.Position(s.Pos()).Line})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if n.Name != "_" {
							f.Defs = append(f.Defs, Symbol{Name: n.Name, Kind: kind, Line: fset.Position(n.Pos()).Line})
						}
					}
				}
			}
		}
	}
	// Package qualifiers ("textfmt.Truncate") are not symbol uses: skip the
	// import names, and the identifier right before a selector dot.
	imports := map[string]bool{}
	for _, im := range af.Imports {
		name := strings.Trim(im.Path.Value, `"`)
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if im.Name != nil {
			name = im.Name.Name
		}
		imports[name] = true
	}
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && imports[id.Name] {
				if len(x.Sel.Name) >= 3 {
					f.refs[x.Sel.Name]++
				}
				return false
			}
		case *ast.Ident:
			if len(x.Name) >= 3 && !imports[x.Name] {
				f.refs[x.Name]++
			}
		}
		return true
	})
	return true
}

func recvName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	}
	return "?"
}

func dedupe(defs []Symbol) []Symbol {
	seen := map[string]bool{}
	out := defs[:0]
	for _, d := range defs {
		if !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d)
		}
	}
	return out
}

// link builds the edges: file A → file B for each symbol A uses that B
// defines (methods match on their bare name). Symbols defined in many
// files are too generic and are skipped; references from a file to
// itself do not count.
func (ix *Index) link() {
	bare := map[string][]int{} // method bare name → files
	for name, files := range ix.definers {
		if i := strings.IndexByte(name, '.'); i >= 0 {
			bare[name[i+1:]] = append(bare[name[i+1:]], files...)
		}
	}
	// A bare method name is a weaker signal than a unique symbol: "Close"
	// on three types says little about which file is meant.
	definers := func(name string) (files []int, weight float64) {
		if exact := ix.definers[name]; len(exact) > 0 {
			return uniq(exact), 1
		}
		return uniq(bare[name]), 0.5
	}
	n := len(ix.Files)
	ix.out = make([][]edge, n)
	ix.in = make([][]edge, n)
	for i, f := range ix.Files {
		own := map[string]bool{}
		for _, d := range f.Defs {
			own[d.Name] = true
			if j := strings.IndexByte(d.Name, '.'); j >= 0 {
				own[d.Name[j+1:]] = true
			}
		}
		weights := map[int]float64{}
		for name, count := range f.refs {
			if own[name] {
				continue // its own definition, not a dependency
			}
			targets, strength := definers(name)
			if len(targets) == 0 || len(targets) > maxDefiners {
				continue
			}
			w := strength * math.Min(float64(count), 5) / float64(len(targets))
			for _, t := range targets {
				if t == i {
					continue
				}
				weights[t] += w
				for k := range ix.Files[t].Defs {
					d := &ix.Files[t].Defs[k]
					if d.Name == name || strings.HasSuffix(d.Name, "."+name) {
						d.Refs += count
					}
				}
			}
		}
		for t, w := range weights {
			ix.out[i] = append(ix.out[i], edge{to: t, weight: w})
			ix.in[t] = append(ix.in[t], edge{to: i, weight: w})
		}
		sort.Slice(ix.out[i], func(a, b int) bool { return ix.out[i][a].to < ix.out[i][b].to })
	}
	for _, f := range ix.Files {
		sort.SliceStable(f.Defs, func(a, b int) bool { return f.Defs[a].Refs > f.Defs[b].Refs })
	}
}

// uniq sorts and de-duplicates without touching its argument. It used to
// compact in place, which was fine for the copies link() hands it and
// quietly wrong in BlastRadius, where the argument is the index's own
// definers slice: looking up a symbol's blast radius reordered and
// rewrote part of the graph it was querying.
func uniq(xs []int) []int {
	if len(xs) == 0 {
		return nil
	}
	sorted := append([]int(nil), xs...)
	sort.Ints(sorted)
	out := sorted[:0]
	for i, x := range sorted {
		if i == 0 || x != sorted[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// Ranked is one file with its score.
type Ranked struct {
	File  *File
	Score float64
	// Why lists the personalisation hits (path, word, symbol, changed).
	Why []string
}

// Personalisation weights: how strongly a request mention pulls a file.
const (
	weightPath    = 3.0
	weightPart    = 2.0
	weightSymbol  = 1.5
	weightChanged = 1.5
	damping       = 0.85
	iterations    = 20
)

var stopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "into": true, "add": true, "make": true, "use": true, "file": true, "files": true, "function": true, "code": true, "test": true, "tests": true, "please": true, "should": true, "when": true, "then": true, "what": true, "where": true, "which": true, "have": true, "does": true, "not": true, "all": true, "new": true, "can": true, "you": true, "your": true, "are": true, "its": true, "also": true, "each": true, "only": true, "same": true, "how": true, "why": true, "fix": true, "bug": true, "change": true, "update": true, "write": true, "read": true}

// Rank orders files for a request. changed lists files (relative paths)
// that differ from HEAD, which get a pull of their own.
func (ix *Index) Rank(query string, changed []string) []Ranked {
	n := len(ix.Files)
	if n == 0 {
		return nil
	}
	words := queryWords(query)
	lower := strings.ToLower(query)
	changedSet := map[string]bool{}
	for _, c := range changed {
		changedSet[filepath.ToSlash(c)] = true
	}
	pers := make([]float64, n)
	why := make([][]string, n)
	total := 0.0
	for i, f := range ix.Files {
		p := 1.0
		lp := strings.ToLower(f.Path)
		base := strings.TrimSuffix(filepath.Base(lp), filepath.Ext(lp))
		if strings.Contains(lower, lp) || (len(base) >= 3 && containsWord(lower, base)) {
			p += weightPath
			why[i] = append(why[i], "path")
		} else {
			parts := strings.Split(strings.TrimSuffix(lp, filepath.Ext(lp)), "/")
			for k, part := range parts {
				if len(part) >= 3 && words[part] {
					p += weightPart
					label := "dir "
					if k == len(parts)-1 {
						label = "name "
					}
					why[i] = append(why[i], label+part)
					break
				}
			}
		}
		for _, d := range f.Defs {
			name := strings.ToLower(d.Name)
			if i := strings.IndexByte(name, '.'); i >= 0 {
				name = name[i+1:]
			}
			if symbolMentioned(name, words) {
				p += weightSymbol
				why[i] = append(why[i], "symbol "+d.Name)
				break
			}
		}
		if changedSet[f.Path] {
			p += weightChanged
			why[i] = append(why[i], "changed")
		}
		pers[i] = p
		total += p
	}
	pull := make([]float64, n) // the request's pull, applied after the walk
	for i := range pers {
		pull[i] = pers[i]
		pers[i] /= total
	}
	score := make([]float64, n)
	copy(score, pers)
	next := make([]float64, n)
	for it := 0; it < iterations; it++ {
		sink := 0.0
		for i := range next {
			next[i] = 0
		}
		for i := range ix.Files {
			outW := 0.0
			for _, e := range ix.out[i] {
				outW += e.weight
			}
			if outW == 0 {
				sink += score[i]
				continue
			}
			for _, e := range ix.out[i] {
				next[e.to] += score[i] * e.weight / outW
			}
		}
		for i := range next {
			score[i] = (1-damping)*pers[i] + damping*(next[i]+sink*pers[i])
		}
	}
	out := make([]Ranked, n)
	for i, f := range ix.Files {
		// Structure decides among unmentioned files; a mention multiplies,
		// so what the request names comes first without drowning the graph.
		// Tests trail the code they test unless the request names them.
		sc := score[i] * pull[i]
		if isTestPath(f.Path) && len(why[i]) == 0 {
			sc *= 0.5
		}
		out[i] = Ranked{File: f, Score: sc, Why: why[i]}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].File.Path < out[b].File.Path
	})
	return out
}

// symbolMentioned matches a symbol against the request's words: exactly
// for names of five letters or more, and by a shared five-letter stem
// so "compaction" finds Compact and "verifying" finds verifyGate. Short
// names (Run, line, tool) are too common to count.
func symbolMentioned(name string, words map[string]bool) bool {
	if len(name) < 5 {
		return false
	}
	if words[name] {
		return true
	}
	for w := range words {
		if len(w) >= 5 && (strings.HasPrefix(w, name) || strings.HasPrefix(name, w)) {
			return true
		}
	}
	return false
}

func queryWords(q string) map[string]bool {
	words := map[string]bool{}
	for _, w := range reIdent.FindAllString(strings.ToLower(q), -1) {
		if !stopwords[w] {
			words[w] = true
		}
	}
	// CamelCase and snake_case mentions also count by their parts.
	for _, w := range reIdent.FindAllString(q, -1) {
		for _, part := range splitIdent(w) {
			if len(part) >= 3 && !stopwords[part] {
				words[part] = true
			}
		}
	}
	return words
}

func splitIdent(s string) []string {
	var parts []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || s[i] == '_' || (s[i] >= 'A' && s[i] <= 'Z' && s[i-1] >= 'a' && s[i-1] <= 'z') {
			if p := strings.ToLower(strings.Trim(s[start:i], "_")); p != "" {
				parts = append(parts, p)
			}
			start = i
		}
	}
	return parts
}

func containsWord(text, word string) bool {
	i := 0
	for {
		j := strings.Index(text[i:], word)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !IsIdentByte(text[j-1])
		after := j+len(word) == len(text) || !IsIdentByte(text[j+len(word)])
		if before && after {
			return true
		}
		i = j + 1
	}
}

// IsIdentByte reports whether c can be part of an ASCII identifier
// (letters, digits, underscore).
func IsIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// moreFooter closes a map that did not fit, and its length is reserved
// from the budget while lines are being added.
const moreFooter = "… %d more files; the repo_map tool ranks for another query or shows a symbol's blast radius\n"

// Render writes the top of a ranking within a byte budget: one line per
// file with its most referenced symbols. No file line is added that would
// leave no room for the closing "N more files" line, so budget bounds the
// whole map and not just the part before the footer. The heading plus
// that footer is the floor: a budget under about 200 bytes buys the two
// of them and no files.
func (ix *Index) Render(ranked []Ranked, budget int) string {
	if len(ranked) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[repository map: %d source files, ranked for this request; symbols ordered by how often other files use them]\n", len(ix.Files))
	shown := 0
	for _, r := range ranked {
		line := r.File.Path
		if len(r.File.Defs) > 0 {
			names := make([]string, 0, 8)
			for _, d := range r.File.Defs {
				if len(names) == 8 {
					break
				}
				names = append(names, d.Name)
			}
			line += ": " + strings.Join(names, ", ")
			if more := len(r.File.Defs) - len(names); more > 0 {
				line += fmt.Sprintf(" (+%d more)", more)
			}
		}
		if len(r.Why) > 0 {
			line += "  ← " + strings.Join(r.Why, ", ")
		}
		// Leave room for the footer: it is written whenever anything was
		// left out, so counting it only at the end let the map overrun
		// the budget it is attached to the user turn under.
		if sb.Len()+len(line)+1+len(moreFooter) > budget {
			break
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		shown++
	}
	if shown < len(ranked) {
		fmt.Fprintf(&sb, moreFooter, len(ranked)-shown)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Radius is what defines and what uses a symbol or file.
type Radius struct {
	Target      string
	Definers    []string // files defining the symbol (or the file itself)
	Sites       []DefSite
	Referencers []Use    // files using it
	Uses        []string // for a file target: files it depends on
}

// DefSite is one definition of a symbol, with the line the parser found.
type DefSite struct {
	Path string
	Name string
	Kind string
	Line int
}

// Use is one referencing file with a weight.
type Use struct {
	Path   string
	Weight float64
}

// BlastRadius resolves a symbol name or a path. transitive adds the
// files that use the referencers too.
func (ix *Index) BlastRadius(target string, transitive bool) (Radius, bool) {
	r := Radius{Target: target}
	target = strings.TrimSpace(target)
	if i, ok := ix.byPath[filepath.ToSlash(target)]; ok {
		r.Definers = []string{ix.Files[i].Path}
		for _, d := range ix.Files[i].Defs {
			r.Sites = append(r.Sites, DefSite{Path: ix.Files[i].Path, Name: d.Name, Kind: d.Kind, Line: d.Line})
		}
		for _, e := range ix.in[i] {
			r.Referencers = append(r.Referencers, Use{ix.Files[e.to].Path, e.weight})
		}
		for _, e := range ix.out[i] {
			r.Uses = append(r.Uses, ix.Files[e.to].Path)
		}
	} else {
		files := ix.definers[target]
		if len(files) == 0 {
			for name, fs := range ix.definers {
				if strings.HasSuffix(name, "."+target) {
					files = append(files, fs...)
				}
			}
		}
		if len(files) == 0 {
			return r, false
		}
		files = uniq(files)
		defSet := map[int]bool{}
		for _, f := range files {
			r.Definers = append(r.Definers, ix.Files[f].Path)
			defSet[f] = true
			for _, d := range ix.Files[f].Defs {
				n := d.Name
				if i := strings.IndexByte(n, '.'); i >= 0 {
					n = n[i+1:]
				}
				if strings.EqualFold(d.Name, target) || strings.EqualFold(n, target) {
					r.Sites = append(r.Sites, DefSite{Path: ix.Files[f].Path, Name: d.Name, Kind: d.Kind, Line: d.Line})
				}
			}
		}
		bare := target
		if i := strings.IndexByte(bare, '.'); i >= 0 {
			bare = bare[i+1:]
		}
		for i, f := range ix.Files {
			if defSet[i] {
				continue
			}
			if c := f.refs[bare]; c > 0 {
				r.Referencers = append(r.Referencers, Use{f.Path, float64(c)})
			}
		}
	}
	if transitive {
		seen := map[string]bool{}
		for _, u := range r.Referencers {
			seen[u.Path] = true
		}
		for _, u := range append([]Use(nil), r.Referencers...) {
			i := ix.byPath[u.Path]
			for _, e := range ix.in[i] {
				p := ix.Files[e.to].Path
				if !seen[p] && !contains(r.Definers, p) {
					seen[p] = true
					r.Referencers = append(r.Referencers, Use{p, e.weight / 2})
				}
			}
		}
	}
	sort.SliceStable(r.Referencers, func(a, b int) bool {
		if r.Referencers[a].Weight != r.Referencers[b].Weight {
			return r.Referencers[a].Weight > r.Referencers[b].Weight
		}
		return r.Referencers[a].Path < r.Referencers[b].Path
	})
	sort.Strings(r.Uses)
	return r, true
}

func isTestPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains("/"+p, "/testdata/") || strings.Contains("/"+p, "/tests/")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Cache rebuilds the index when the tree changes.
type Cache struct {
	Root string
	// Parser, when set, is the grammar-backed parser the index uses. The
	// composition root supplies it; nothing here registers itself.
	Parser Parser
	mu     sync.Mutex
	ix     *Index
}

// Get returns the current index, rebuilding it when files changed.
func (c *Cache) Get(ctx context.Context) (*Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ix != nil && c.ix.Fingerprint == gitx.TreeFingerprint(c.Root) {
		return c.ix, nil
	}
	ix, err := Build(ctx, c.Root, c.Parser)
	if err != nil {
		return nil, err
	}
	c.ix = ix
	return ix, nil
}

// ChangedFiles lists paths that differ from HEAD (git status), for the
// personalisation.
func ChangedFiles(ctx context.Context, root string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain", "-z").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range strings.Split(string(out), "\x00") {
		if len(entry) > 3 {
			paths = append(paths, entry[3:])
		}
	}
	return paths
}

// Defs parses one file's definitions from content, the way Build indexes
// a file on disk: for a caller that has content not yet written, such as
// the planned result of an edit.
func Defs(path string, content []byte, sym Parser) []Symbol {
	return parse(path, content, sym).Defs
}

// Parser is the grammar-backed parser this index was built with, so a
// caller that has content not yet written can parse it the same way.
func (ix *Index) Parser() Parser {
	if ix == nil {
		return nil
	}
	return ix.sym
}

// SetRoot points the cache at another directory and drops the index built
// for the old one. /cd calls it: a map of the tree the session has left is
// worse than no map, because it names files the model can no longer open.
func (c *Cache) SetRoot(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Root, c.ix = root, nil
}
