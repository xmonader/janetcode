package typosquat

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/depcheck"
)

// Name is one dependency name an edit added.
type Name struct {
	Eco, Name string
}

// Finding is an added name that resembles a known one it is not.
type Finding struct {
	Eco, Name string
	// Like is the known name it resembles, "" for KindNonASCII alone.
	Like string
	// Kind says how (one of the Kind constants), Source where Like was
	// found (one of the Src constants).
	Kind, Source string
}

var kindRank = map[string]int{KindCase: 0, KindLookalike: 1, KindSeparator: 2, KindAffix: 3, KindEdit: 4}

// Check compares one added name with what k knows and returns the
// closest resemblance, if any. A name k knows is never flagged.
func Check(k *Known, n Name) (Finding, bool) {
	name := normalize(n.Eco, n.Name)
	if name == "" || k.Has(n.Eco, name) {
		return Finding{}, false
	}
	best, found := Finding{}, false
	consider := func(like, kind, src string) {
		if kind == "" {
			return
		}
		f := Finding{Eco: n.Eco, Name: n.Name, Like: like, Kind: kind, Source: src}
		if !found || kindRank[kind] < kindRank[best.Kind] ||
			(kindRank[kind] == kindRank[best.Kind] && srcRank[src] < srcRank[best.Source]) ||
			(kindRank[kind] == kindRank[best.Kind] && srcRank[src] == srcRank[best.Source] && like < best.Like) {
			best, found = f, true
		}
	}
	for like, src := range k.m[n.Eco] {
		if n.Eco == Go {
			if goKnownPrefix(name, like) {
				return Finding{}, false
			}
			consider(like, resembleGo(name, like), src)
			continue
		}
		consider(like, Resemble(name, like), src)
	}
	if !found && nonASCII(name) {
		return Finding{Eco: n.Eco, Name: n.Name, Kind: KindNonASCII}, true
	}
	return best, found
}

// goKnownPrefix reports an import path inside a known module.
func goKnownPrefix(path, mod string) bool {
	return strings.HasPrefix(path, mod+"/")
}

// resembleGo compares an import path with a known module path at the
// module's depth: the import's first as many elements as the module has.
// A path that matches but for one element is judged by that element
// (boltdb-go/bolt for boltdb/bolt, spf14/cobra for spf13/cobra); case
// and lookalikes are judged on the whole.
func resembleGo(path, mod string) string {
	ms := strings.Split(mod, "/")
	ps := strings.Split(path, "/")
	if len(ps) < len(ms) {
		return ""
	}
	cand := strings.Join(ps[:len(ms)], "/")
	if cand == mod {
		return ""
	}
	if r := Resemble(cand, mod); r == KindCase || r == KindLookalike || r == KindSeparator {
		return r
	}
	diff := -1
	for i := range ms {
		if ps[i] != ms[i] {
			if diff >= 0 {
				return ""
			}
			diff = i
		}
	}
	if diff <= 0 {
		return "" // a different host is a different module, not a typo
	}
	return Resemble(ps[diff], ms[diff])
}

// Added lists the dependency names the edit from before to after of the
// file at path brings in: new requirements of a manifest, and new
// imports of Go, Python and JavaScript/TypeScript source. Relative
// imports, the Go standard library and Node's node: scheme are not
// dependencies and are left out.
func Added(path string, before, after []byte) []Name {
	if eco := depcheck.Ecosystem(path); eco != "" {
		var out []Name
		for _, n := range depcheck.Added(eco, before, after) {
			out = append(out, Name{Eco: eco, Name: n})
		}
		return out
	}
	eco, parse := importer(path)
	if parse == nil {
		return nil
	}
	had := map[string]bool{}
	for _, n := range parse(before) {
		had[n] = true
	}
	var out []Name
	for _, n := range parse(after) {
		if !had[n] {
			had[n] = true
			out = append(out, Name{Eco: eco, Name: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Source reports whether path is a file Added reads imports or
// requirements from.
func Source(path string) bool {
	if depcheck.Ecosystem(path) != "" {
		return true
	}
	_, p := importer(path)
	return p != nil
}

func importer(path string) (string, func([]byte) []string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return Go, goImports
	case ".py":
		return PyImport, pyImports
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte":
		return NPM, jsImports
	}
	return "", nil
}

// goImports lists the non-standard import paths of a Go file. A file
// that does not parse (an edit half way) falls back to the quoted paths
// of its import lines.
func goImports(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	var paths []string
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err == nil {
		for _, im := range f.Imports {
			if p, err := strconv.Unquote(im.Path.Value); err == nil {
				paths = append(paths, p)
			}
		}
	} else {
		for _, m := range goImportLine.FindAllSubmatch(src, -1) {
			paths = append(paths, string(m[1]))
		}
	}
	var out []string
	for _, p := range paths {
		first := strings.SplitN(p, "/", 2)[0]
		if strings.Contains(first, ".") {
			out = append(out, p)
		}
	}
	return out
}

var goImportLine = regexp.MustCompile(`(?m)^\s*(?:import\s+)?(?:[\w.]+\s+)?"([^"\s]+)"\s*$`)

var (
	pyImportRe = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([\w., \t]+)`)
	pyFromRe   = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+([\w.]+)[ \t]+import\b`)
)

// pyImports lists the top-level modules a Python file imports.
func pyImports(src []byte) []string {
	var out []string
	for _, m := range pyFromRe.FindAllSubmatch(src, -1) {
		if mod := string(m[1]); !strings.HasPrefix(mod, ".") {
			out = append(out, strings.SplitN(mod, ".", 2)[0])
		}
	}
	for _, m := range pyImportRe.FindAllSubmatch(src, -1) {
		for _, part := range strings.Split(string(m[1]), ",") {
			f := strings.Fields(part)
			if len(f) == 0 {
				continue
			}
			out = append(out, strings.SplitN(f[0], ".", 2)[0])
		}
	}
	return out
}

var jsImportRe = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(?\s*|\brequire\s*\(\s*)['"]([^'"\s]+)['"]`)

// jsImports lists the packages a JavaScript or TypeScript file imports:
// bare specifiers only, cut to the package (@scope/name, or name).
func jsImports(src []byte) []string {
	var out []string
	for _, m := range jsImportRe.FindAllSubmatch(src, -1) {
		s := string(m[1])
		switch {
		case strings.HasPrefix(s, "."), strings.HasPrefix(s, "/"), strings.HasPrefix(s, "#"),
			strings.HasPrefix(s, "~"), strings.HasPrefix(s, "@/"), strings.Contains(s, ":"):
			continue
		}
		parts := strings.Split(s, "/")
		if strings.HasPrefix(s, "@") {
			if len(parts) < 2 {
				continue
			}
			out = append(out, parts[0]+"/"+parts[1])
			continue
		}
		out = append(out, parts[0])
	}
	return out
}

var ecoName = map[string]string{Go: "Go", NPM: "npm", PyPI: "PyPI", PyImport: "Python"}

// Note is the line the findings add to an edit's result, "" for none.
func Note(path string, fs []Finding) string {
	if len(fs) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[typosquat check] %s adds:", filepath.Base(path))
	for _, f := range fs {
		if f.Kind == KindNonASCII {
			fmt.Fprintf(&sb, "\n- %q %s, which no %s package name has; it is likely a lookalike of a real one.", f.Name, KindNonASCII, ecoName[f.Eco])
			continue
		}
		fmt.Fprintf(&sb, "\n- %s %s %s (%s).", f.Name, f.Kind, f.Like, f.Source)
	}
	sb.WriteString("\nA near-miss of a real name is how typosquatted packages get installed. Check the spelling is the package you mean before installing or building on it.")
	return sb.String()
}
