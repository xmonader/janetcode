package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/treesitter"
)

// AstEdit replaces a whole named definition (function, method, struct,
// class, type...) by the byte range of its syntax node, and adds an import
// idempotently.
//
// Ported from hero_agent (tools/ast_edit.rs, hero_agent_langs items/tags
// queries). It is additive: edit_file stays the default; this is for
// "rewrite this symbol" edits where a string anchor would be ambiguous or
// brittle. Safety net, as in hero:
//   - uniqueness: refuses unless exactly one definition matches the name;
//   - post-parse gate: the result is parsed again and, if the edit added
//     syntax errors, nothing is written (a rollback by never writing);
//   - the same seen / permission / checkpoint / diagnostics path as
//     edit_file (it is an edit tool to every gate that looks at tools).
//
// Backends: Go through go/parser (always available); C, JavaScript,
// TypeScript, TSX, Python and Rust through internal/treesitter when built
// with -tags treesitter. A file no backend handles is refused with the list
// of what is handled. Off: JANETCODE_OFF=astedit (the tool is not offered).
type AstEdit struct {
	Root string
	WS   *Workspace
	Seen *Seen
	// Cache is dropped for the file after a write.
	Cache *IOCache
}

func (AstEdit) Name() string { return "ast_edit" }
func (AstEdit) Description() string {
	return "Replace one whole named definition (function, method, type, class, struct...) in a file with new source, located by its syntax node rather than by text: give path, symbol and new_source (the entire new definition). Refused unless exactly one definition has that name (a Go method can be given as Type.Name); refused, and nothing written, if the result would have new syntax errors. Doc comments and attributes above the definition are kept. add_import idempotently adds an import line (e.g. `import os`, `use std::fmt;`, `\"fmt\"`) after the existing imports and does nothing if it is present; it needs no symbol. Works for Go always, and for C, JavaScript, TypeScript, Python and Rust when the build has the grammars. Use edit_file for anything smaller than a definition."
}
func (AstEdit) Mutating() bool { return true }
func (AstEdit) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path; read it first"},
"symbol":{"type":"string","description":"Name of the definition to replace"},
"new_source":{"type":"string","description":"The entire new definition, from its first keyword (or decorator/export) to its closing brace"},
"add_import":{"type":"string","description":"An import line to add if missing, written as it appears in the file's language"}},
"required":["path"],"additionalProperties":false}`)
}

type astInput struct {
	Path      string  `json:"path"`
	Symbol    string  `json:"symbol"`
	NewSource *string `json:"new_source"`
	AddImport string  `json:"add_import"`
}

// astDef is one definition: its names and byte range.
type astDef struct {
	names      []string
	start, end int
	line       int
}

// AstSupported reports whether some backend handles the file.
func AstSupported(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".go") || treesitter.Supported(path)
}

func astDefs(path string, src []byte) ([]astDef, bool) {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return goDefs(src)
	}
	ds, ok := treesitter.Definitions(path, src)
	if !ok {
		return nil, false
	}
	out := make([]astDef, len(ds))
	for i, d := range ds {
		out[i] = astDef{names: []string{d.Name}, start: d.Start, end: d.End, line: d.Line}
	}
	return out, true
}

func astErrors(path string, src []byte) (int, bool) {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		_, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
		if el, ok := err.(scanner.ErrorList); ok {
			return len(el), true
		}
		if err != nil {
			return 1, true
		}
		return 0, true
	}
	return treesitter.SyntaxErrors(path, src)
}

func goDefs(src []byte) ([]astDef, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution|parser.ParseComments)
	if f == nil {
		return nil, false
	}
	_ = err // a file with errors still names what it declares
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var out []astDef
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			names := []string{d.Name.Name}
			if d.Recv != nil && len(d.Recv.List) == 1 {
				names = append(names, recvName(d.Recv.List[0].Type)+"."+d.Name.Name)
			}
			out = append(out, astDef{names: names, start: off(d.Pos()), end: off(d.End()), line: fset.Position(d.Pos()).Line})
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, sp := range d.Specs {
				var names []string
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					names = []string{sp.Name.Name}
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						names = append(names, n.Name)
					}
				}
				if len(names) == 0 {
					continue
				}
				// A lone spec is replaced with its keyword; one of a group
				// by itself, so its neighbours are untouched.
				start, end := off(sp.Pos()), off(sp.End())
				if len(d.Specs) == 1 && !d.Lparen.IsValid() {
					start, end = off(d.Pos()), off(d.End())
				}
				out = append(out, astDef{names: names, start: start, end: end, line: fset.Position(sp.Pos()).Line})
			}
		}
	}
	return out, true
}

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
	return ""
}

func (t AstEdit) checkSeen(ctx context.Context, input json.RawMessage) error {
	var in astInput
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return nil
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return nil
	}
	return t.Seen.CheckAt(ctx, t.WS, p, displayPath(rootOf(t.Root, t.WS), in.Path), "edit")
}

func (t AstEdit) plan(ctx context.Context, input json.RawMessage) (*editPlan, error) {
	var in astInput
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if in.Symbol == "" && in.AddImport == "" {
		return nil, fmt.Errorf("nothing to do: give symbol and new_source, or add_import")
	}
	if in.Symbol != "" && in.NewSource == nil {
		return nil, fmt.Errorf("symbol %q needs new_source, the entire new definition", in.Symbol)
	}
	if in.Symbol == "" && in.NewSource != nil {
		return nil, fmt.Errorf("new_source needs symbol, the name of the definition it replaces")
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return nil, err
	}
	display := displayPath(rootOf(t.Root, t.WS), in.Path)
	if !AstSupported(p) {
		return nil, fmt.Errorf("ast_edit cannot parse %s: it handles .go%s files; use edit_file", display, treesitterExts())
	}
	data, err := readFileOf(ctx, t.WS, p)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, &ErrPriorRead{Path: display, Verb: "edit", Cause: CauseBinary}
	}
	before, shape := decodeText(data)
	errsBefore, ok := astErrors(p, []byte(before))
	if !ok {
		return nil, fmt.Errorf("ast_edit cannot parse %s (too large or no grammar); use edit_file", display)
	}
	after, n := before, 0
	if in.Symbol != "" {
		defs, _ := astDefs(p, []byte(after))
		var hit []astDef
		for _, d := range defs {
			for _, nm := range d.names {
				if nm == in.Symbol {
					hit = append(hit, d)
					break
				}
			}
		}
		switch {
		case len(hit) == 0:
			return nil, fmt.Errorf("no definition named %q in %s; names it has: %s", in.Symbol, display, listDefNames(defs))
		case len(hit) > 1:
			var lines []string
			for _, d := range hit {
				lines = append(lines, strconv.Itoa(d.line))
			}
			return nil, fmt.Errorf("%d definitions named %q in %s (lines %s); refusing to guess. Use edit_file with enough context to pick one", len(hit), in.Symbol, display, strings.Join(lines, ", "))
		}
		d := hit[0]
		ns := strings.TrimRight(*in.NewSource, "\r\n")
		after = after[:d.start] + ns + after[d.end:]
		n = 1
	}
	if in.AddImport != "" {
		withImport, changed, err := addImport(p, after, in.AddImport)
		if err != nil {
			return nil, err
		}
		if changed {
			after = withImport
			n++
		}
	}
	if after == before {
		return nil, fmt.Errorf("no change: the file already has that")
	}
	if errsAfter, ok := astErrors(p, []byte(after)); !ok || errsAfter > errsBefore {
		return nil, fmt.Errorf("rolled back, nothing written: the edit would leave %s with %d syntax error(s) (it had %d). Check the braces, indentation and quoting of new_source", display, errsAfter, errsBefore)
	}
	if err := checkWriteSize("the edited "+display, len(after), "a file this large has to be edited with a bash command instead"); err != nil {
		return nil, err
	}
	return &editPlan{path: p, display: display, before: before, after: after, replacements: n, shape: shape}, nil
}

func treesitterExts() string {
	if treesitter.Supported("x.py") {
		return ", .c, .h, .js, .jsx, .ts, .tsx, .py and .rs"
	}
	return " (this build has no tree-sitter grammars for other languages)"
}

func listDefNames(defs []astDef) string {
	var names []string
	for _, d := range defs {
		names = append(names, d.names[0])
	}
	if len(names) == 0 {
		return "(none)"
	}
	if len(names) > 40 {
		names = append(names[:40], "...")
	}
	return strings.Join(names, ", ")
}

var (
	importStart = map[string]*regexp.Regexp{
		".py":  regexp.MustCompile(`^(import|from)\s`),
		".js":  regexp.MustCompile(`^import\b`),
		".ts":  regexp.MustCompile(`^import\b`),
		".rs":  regexp.MustCompile(`^(pub\s+)?use\s`),
		".c":   regexp.MustCompile(`^\s*#\s*include\b`),
		".mjs": regexp.MustCompile(`^import\b`),
	}
	importAlias = map[string]string{".jsx": ".js", ".cjs": ".js", ".mts": ".ts", ".cts": ".ts", ".tsx": ".ts", ".h": ".c"}
)

// addImport returns src with the import line added after the last import,
// and whether it changed anything.
func addImport(path, src, line string) (string, bool, error) {
	line = strings.TrimRight(strings.TrimSpace(line), "\r\n")
	if line == "" {
		return src, false, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		return addGoImport(src, line)
	}
	if a, ok := importAlias[ext]; ok {
		ext = a
	}
	re := importStart[ext]
	if re == nil {
		return "", false, fmt.Errorf("add_import does not know the import syntax of %s files", ext)
	}
	lines := strings.Split(src, "\n")
	last := -1
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == line {
			return src, false, nil
		}
		if !re.MatchString(lines[i]) {
			continue
		}
		// a statement over several lines ends where its brackets close
		depth := strings.Count(lines[i], "(") + strings.Count(lines[i], "{") - strings.Count(lines[i], ")") - strings.Count(lines[i], "}")
		j := i
		for depth > 0 && j+1 < len(lines) {
			j++
			depth += strings.Count(lines[j], "(") + strings.Count(lines[j], "{") - strings.Count(lines[j], ")") - strings.Count(lines[j], "}")
		}
		last, i = j, j
	}
	at := 0
	if last >= 0 {
		at = last + 1
	} else if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		at = 1
	}
	out := append(append(append([]string{}, lines[:at]...), line), lines[at:]...)
	return strings.Join(out, "\n"), true, nil
}

func addGoImport(src, line string) (string, bool, error) {
	spec := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "import"))
	spec = strings.Trim(spec, "() \t")
	pathLit := spec
	if i := strings.LastIndexAny(spec, " \t"); i >= 0 {
		pathLit = spec[i+1:]
	}
	want, err := strconv.Unquote(pathLit)
	if err != nil {
		return "", false, fmt.Errorf("add_import for Go wants an import path such as \"fmt\" or `import \"fmt\"`, got %q", line)
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "", src, parser.ImportsOnly|parser.SkipObjectResolution)
	if f == nil {
		return "", false, fmt.Errorf("add_import: cannot parse the file: %v", perr)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var lastGroup *ast.GenDecl
	for _, im := range f.Imports {
		if got, _ := strconv.Unquote(im.Path.Value); got == want && (im.Name == nil || strings.HasPrefix(spec, im.Name.Name+" ")) {
			return src, false, nil
		}
	}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			lastGroup = g
			if g.Lparen.IsValid() {
				break
			}
		}
	}
	var b bytes.Buffer
	switch {
	case lastGroup != nil && lastGroup.Lparen.IsValid():
		at := off(lastGroup.Rparen)
		b.WriteString(src[:at])
		b.WriteString("\t" + spec + "\n")
		b.WriteString(src[at:])
	case lastGroup != nil:
		old := strings.TrimSpace(src[off(lastGroup.Pos()):off(lastGroup.End())])
		old = strings.TrimSpace(strings.TrimPrefix(old, "import"))
		b.WriteString(src[:off(lastGroup.Pos())])
		b.WriteString("import (\n\t" + old + "\n\t" + spec + "\n)")
		b.WriteString(src[off(lastGroup.End()):])
	default:
		at := off(f.Name.End())
		b.WriteString(src[:at])
		b.WriteString("\n\nimport " + spec)
		b.WriteString(src[at:])
	}
	return b.String(), true, nil
}

func (t AstEdit) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if err := t.checkSeen(ctx, input); err != nil {
		return "", err
	}
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	data, err := encodeChecked(pl.after, pl.shape, "the edited "+pl.display, "a file this large has to be edited with a bash command instead")
	if err != nil {
		return "", err
	}
	if err := writeFileOf(ctx, t.WS, pl.path, data); err != nil {
		return "", err
	}
	t.Cache.Drop(pl.path)
	t.Seen.Record(pl.path, data)
	added, removed := diff.Stats(pl.before, pl.after)
	out := fmt.Sprintf("Edited %s by syntax node (%s, +%d -%d lines; re-parsed with no new syntax errors)", pl.display, textfmt.Plural(pl.replacements, "change"), added, removed)
	if note := pl.shape.Note(); note != "" {
		out += "\n[" + note + "]"
	}
	if first, last, ok := changed(pl.before, pl.after); ok {
		if w := window(pl.after, first, last); w != "" {
			out += "\n\n" + w
		}
	}
	return out, nil
}

// Guard implements Guard, as edit_file does.
func (t AstEdit) Guard(input json.RawMessage) (bool, string) {
	return guardSelfModify(rootOf(t.Root, t.WS), input)
}

// Touches reports the file the edit will rewrite.
func (t AstEdit) Touches(input json.RawMessage) ([]string, error) {
	return touchedPathOn(t.WS, t.Root, input)
}

// Preview implements Previewer with a unified diff.
func (t AstEdit) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	return diff.Unified(pl.display, pl.before, pl.after, 3), nil
}

// Result is what applying the edit would produce, as EditFile.Result.
func (t AstEdit) Result(ctx context.Context, input json.RawMessage) (path, before, after string, err error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", "", "", err
	}
	return pl.path, pl.before, pl.after, nil
}
