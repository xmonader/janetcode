package preresolve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// Extent is the range of lines (0-based, inclusive) a definition that
// starts at line start takes up in ls, with the comment block right above
// it: Go's own parser for a .go file, else indentation for Python and
// braces for everything else. It never covers more than maxDefLines, and
// a definition it cannot measure is its own line.
func Extent(path string, ls []string, start int) (from, to int) {
	if start < 0 || start >= len(ls) {
		return 0, -1
	}
	from, to = start, start
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		if f, t, ok := goExtent(ls, start); ok {
			from, to = f, t
			break
		}
		to = braceEnd(ls, start)
	case ".py", ".pyi":
		to = indentEnd(ls, start)
	default:
		if strings.HasSuffix(strings.TrimSpace(ls[start]), ":") {
			to = indentEnd(ls, start)
		} else {
			to = braceEnd(ls, start)
		}
	}
	if from == start {
		from = commentAbove(ls, start)
	}
	if to-from+1 > maxDefLines {
		to = from + maxDefLines - 1
	}
	return from, to
}

// goExtent asks go/parser for the declaration at line start: a function
// with its doc comment, or the one spec of a grouped type, var or const
// block that the line names, not the whole block.
func goExtent(ls []string, start int) (from, to int, ok bool) {
	src := strings.Join(ls, "\n")
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return 0, 0, false
	}
	line := start + 1
	span := func(lo, hi token.Pos, doc *ast.CommentGroup) (int, int, bool) {
		if doc != nil {
			lo = doc.Pos()
		}
		return fset.Position(lo).Line - 1, fset.Position(hi).Line - 1, true
	}
	for _, d := range af.Decls {
		if fset.Position(d.Pos()).Line > line || fset.Position(d.End()).Line < line {
			continue
		}
		switch d := d.(type) {
		case *ast.FuncDecl:
			return span(d.Pos(), d.End(), d.Doc)
		case *ast.GenDecl:
			if !d.Lparen.IsValid() {
				return span(d.Pos(), d.End(), d.Doc)
			}
			for _, s := range d.Specs {
				if fset.Position(s.Pos()).Line > line || fset.Position(s.End()).Line < line {
					continue
				}
				switch s := s.(type) {
				case *ast.TypeSpec:
					return span(s.Pos(), s.End(), s.Doc)
				case *ast.ValueSpec:
					return span(s.Pos(), s.End(), s.Doc)
				}
			}
			return span(d.Pos(), d.End(), d.Doc)
		}
	}
	return 0, 0, false
}

// braceEnd is the line on which the braces opened on or after start
// close again. A line that opens none within three lines is a definition
// of one line (a constant, a forward declaration).
func braceEnd(ls []string, start int) int {
	depth, opened := 0, false
	for i := start; i < len(ls) && i-start < maxDefLines; i++ {
		for _, c := range stripStrings(ls[i]) {
			switch c {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			return i
		}
		if !opened && i-start >= 2 {
			return start
		}
		if !opened && strings.HasSuffix(strings.TrimSpace(ls[i]), ";") {
			return i
		}
	}
	if !opened {
		return start
	}
	return min(start+maxDefLines-1, len(ls)-1)
}

// stripStrings blanks quoted text and a trailing // comment, so a brace
// inside either is not counted.
func stripStrings(l string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(l) && l[i+1] == '/':
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// indentEnd is the last line of the block start opens: every following
// line indented deeper than start, blank lines included, trailing blank
// lines not.
func indentEnd(ls []string, start int) int {
	base := indent(ls[start])
	end := start
	// A signature spread over several lines ends at the first line ending
	// in a colon; the body is what follows it.
	i := start + 1
	for j := start; j < len(ls) && j-start < 8; j++ {
		if strings.HasSuffix(strings.TrimSpace(ls[j]), ":") {
			i, end = j+1, j
			break
		}
	}
	for ; i < len(ls); i++ {
		t := strings.TrimSpace(ls[i])
		if t == "" {
			continue
		}
		if indent(ls[i]) <= base {
			break
		}
		end = i
	}
	return end
}

func indent(l string) int {
	n := 0
	for _, c := range l {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// commentAbove walks up over the comment lines and decorators directly
// above start.
func commentAbove(ls []string, start int) int {
	from := start
	for i := start - 1; i >= 0 && start-i <= 20; i-- {
		t := strings.TrimSpace(ls[i])
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "@") || strings.HasPrefix(t, "///") {
			from = i
			continue
		}
		break
	}
	return from
}
