//go:build treesitter

package treesitter

import (
	"path/filepath"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Def is one named definition: the node that declares Name, as a byte
// range of the source. The range is the whole definition (a decorated
// Python function includes its decorators, an exported JavaScript function
// its export), not just the name; attributes and doc comments that are
// siblings of the node (Rust) are not part of it.
type Def struct {
	Name       string
	Kind       string
	Start, End int
	Line       int
}

// declaratorKind nodes sit between a name and the node that declares it
// (C's function_declarator, JavaScript's variable_declarator).
func declaratorKind(k string) bool {
	return k == "function_declarator" || k == "pointer_declarator" || k == "variable_declarator"
}

// containerKind nodes wrap a definition without being different from it:
// the range replaced covers them (a decorator, an export, the const).
func containerKind(k string) bool {
	switch k {
	case "lexical_declaration", "variable_declaration", "decorated_definition", "export_statement":
		return true
	}
	return false
}

// Definitions lists the named definitions of a file, from the same query
// the repository map uses. ok is false when no grammar handles the file.
func Definitions(path string, src []byte) ([]Def, bool) {
	l := languages[strings.ToLower(filepath.Ext(path))]
	if l == nil || len(src) > MaxBytes {
		return nil, false
	}
	if err := l.load(); err != nil {
		return nil, false
	}
	parser := ts.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(l.lang); err != nil {
		return nil, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(l.q, tree.RootNode(), src)
	names := l.q.CaptureNames()
	var out []Def
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, cap := range m.Captures {
			kind := "def"
			if int(cap.Index) < len(names) {
				kind = names[cap.Index]
			}
			n := cap.Node.Parent()
			if n == nil {
				continue
			}
			for declaratorKind(n.Kind()) && n.Parent() != nil {
				n = n.Parent()
			}
			for n.Parent() != nil && containerKind(n.Parent().Kind()) {
				n = n.Parent()
			}
			out = append(out, Def{
				Name: cap.Node.Utf8Text(src), Kind: kind,
				Start: int(n.StartByte()), End: int(n.EndByte()),
				Line: int(n.StartPosition().Row) + 1,
			})
		}
	}
	return out, true
}

// SyntaxErrors counts the ERROR and missing nodes in a parse of src. ok is
// false when no grammar handles the file.
func SyntaxErrors(path string, src []byte) (int, bool) {
	l := languages[strings.ToLower(filepath.Ext(path))]
	if l == nil || len(src) > MaxBytes {
		return 0, false
	}
	if err := l.load(); err != nil {
		return 0, false
	}
	parser := ts.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(l.lang); err != nil {
		return 0, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return 0, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if !root.HasError() {
		return 0, true
	}
	count := 0
	cur := root.Walk()
	defer cur.Close()
	visited := false
	for {
		if !visited {
			n := cur.Node()
			if n.IsError() || n.IsMissing() {
				count++
			}
		}
		if !visited && cur.GotoFirstChild() {
			continue
		}
		if cur.GotoNextSibling() {
			visited = false
			continue
		}
		if !cur.GotoParent() {
			return count, true
		}
		visited = true
	}
}
