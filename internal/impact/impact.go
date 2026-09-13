// Package impact answers one question before a mutating edit is approved:
// what does this touch? It names the symbols whose definitions the edit's
// changed lines fall inside, how many places reference each one, which
// test files already cover them, and which verify commands will run.
//
// The answer comes from whichever source is available: a running language
// server's resolved view of the program (see package lsp), or the
// repository map's regex/AST-based index (see package repomap) when no
// server covers the file. The repository map is always the cheaper,
// less exact source — its "reference" is an identifier count, not a
// resolved use — so an answer built from it is marked Partial, as is one
// cut short by the time budget.
//
// Analyze never writes anything and never reads the file itself: it is
// handed the content before and after the edit, computed by the caller
// (see tool.EditFile.Result and tool.WriteFile.Result).
package impact

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Budget bounds the whole of Analyze. It is a var, not a const, so a test
// can shrink it to keep a "slow source" case fast; production leaves it
// alone. A permission prompt must never wait noticeably for this, so the
// value is short enough that a caller can treat it as free.
var Budget = 800 * time.Millisecond

// Symbol is one definition the edit changes, with how it is used
// elsewhere in the repository.
type Symbol struct {
	Name string
	Kind string
	// Refs is how many places use the symbol, not counting its own
	// definition.
	Refs int
	// RefFiles is how many distinct files those uses are in.
	RefFiles int
}

// Impact is what an edit reaches.
type Impact struct {
	// Symbols are the definitions the edit's changed lines fall inside.
	Symbols []Symbol
	// Tests are the files covering a changed symbol: any referencing file
	// that looks like a test, plus the conventional sibling test file
	// (foo.go → foo_test.go) when one exists, whether or not it turned up
	// as a referencer.
	Tests []string
	// Checks are the project's verify commands (see package verify), named
	// here as what will run, not run by Analyze.
	Checks []string
	// Partial says the answer is missing something: a language server
	// wasn't available and the repository map's coarser index stood in
	// for it, or the time budget ran out before every changed symbol
	// could be looked up.
	Partial bool
}

// Line renders Impact as one compact line for a permission prompt, e.g.
//
//	changes Parse (func) · 14 refs in 6 files · tests: parse_test.go, cli_test.go · checks: go test ./...
//
// It is "" when nothing is known: a non-code file, or an edit that
// touches no recognizable definition.
func (i Impact) Line() string {
	if len(i.Symbols) == 0 {
		return ""
	}
	names := make([]string, len(i.Symbols))
	totalRefs, totalFiles := 0, 0
	for j, s := range i.Symbols {
		label := s.Name
		if s.Kind != "" {
			label += " (" + s.Kind + ")"
		}
		names[j] = label
		totalRefs += s.Refs
		totalFiles += s.RefFiles
	}
	parts := []string{"changes " + strings.Join(names, ", ")}
	parts = append(parts, fmt.Sprintf("%s in %s", textfmt.Plural(totalRefs, "ref"), textfmt.Plural(totalFiles, "file")))
	if len(i.Tests) > 0 {
		parts = append(parts, "tests: "+strings.Join(i.Tests, ", "))
	}
	if len(i.Checks) > 0 {
		parts = append(parts, "checks: "+strings.Join(i.Checks, ", "))
	}
	line := strings.Join(parts, " · ")
	if i.Partial {
		line += " (partial)"
	}
	return line
}

// Source is a language server's resolved view of one file: its
// definitions, and who references one of them. *lsp.Manager satisfies
// this; a fake in tests can too. A nil Source, or one that answers
// nothing for a file (no server configured, none on PATH), simply falls
// through to the repository map — its methods already degrade that way,
// so Analyze does not special-case "no server" itself.
type Source interface {
	// Outline lists a file's declared symbols, in source order.
	Outline(ctx context.Context, path string) ([]lsp.Symbol, error)
	// References lists every use of the symbol at pos, including its
	// declaration.
	References(ctx context.Context, path string, pos lsp.Point) ([]lsp.Location, error)
}

// Input is one edit to analyze.
type Input struct {
	// Path is the file being edited, relative to Cwd or absolute.
	Path string
	// Old and New are the file's content before and after the edit,
	// already computed by the caller: Analyze neither writes nor reads
	// the file.
	Old, New string
	// Cwd is the project root, so Path can be compared against the
	// slash-relative paths LSP and repomap results carry.
	Cwd string
	// LSP answers definition and reference queries; optional.
	LSP Source
	// RepoIndex is the repository's symbol index (see package repomap),
	// used for a file's own definitions when LSP has none, and to count
	// references to a changed one. Optional.
	RepoIndex *repomap.Index
	// Checks are the project's verify commands, reported verbatim.
	Checks []string
}

// def is one file-local definition, from whichever source answered:
// a name, a kind, and the line it starts on. Neither LSP's outline nor
// repomap's index reports where a definition ends, so Analyze
// approximates one: a definition's reach runs from its own line to the
// line before the next definition (in source order), or the end of the
// file for the last one.
type def struct {
	Name, Kind string
	Line       int
}

// Analyze reports what changing Old into New reaches. It always returns
// within Budget of being called, whatever ctx allows and however slow LSP
// or the repository map turn out to be.
func Analyze(ctx context.Context, in Input) Impact {
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()

	out := Impact{Checks: append([]string(nil), in.Checks...)}
	if strings.TrimSpace(in.Old) == "" && strings.TrimSpace(in.New) == "" {
		return out
	}
	fromLine, toLine, changed := changedRange(in.Old, in.New)
	if !changed {
		return out
	}
	rel := relPath(in.Cwd, in.Path)

	defs, partial := fileDefs(ctx, in, rel)
	touched := intersecting(defs, fromLine, toLine)
	if len(touched) == 0 {
		out.Partial = partial
		return out
	}

	tests := map[string]bool{}
	syms := make([]Symbol, 0, len(touched))
	for _, d := range touched {
		refs, files, refPartial := referencesFor(ctx, in, rel, d)
		partial = partial || refPartial
		syms = append(syms, Symbol{Name: d.Name, Kind: d.Kind, Refs: refs, RefFiles: len(files)})
		for _, f := range files {
			if tool.IsTestFile(f) {
				tests[f] = true
			}
		}
		if ctx.Err() != nil {
			partial = true
			break
		}
	}
	if sib := siblingTestFile(in.Cwd, rel); sib != "" {
		tests[sib] = true
	}
	if ctx.Err() != nil {
		partial = true
	}

	out.Symbols = syms
	out.Tests = sortedKeys(tests)
	out.Partial = partial
	return out
}

// fileDefs lists rel's definitions, preferring the language server and
// falling back to the repository map. partial reports whether the
// fallback was the one that actually produced them.
func fileDefs(ctx context.Context, in Input, rel string) (defs []def, partial bool) {
	if in.LSP != nil && ctx.Err() == nil {
		if syms, err := in.LSP.Outline(ctx, rel); err == nil && len(syms) > 0 {
			for _, s := range syms {
				defs = append(defs, def{Name: s.Name, Kind: s.Kind, Line: s.Loc.Line})
			}
			sortDefs(defs)
			return defs, false
		}
	}
	if in.RepoIndex != nil {
		for _, f := range in.RepoIndex.Files {
			if f.Path != rel {
				continue
			}
			for _, s := range f.Defs {
				defs = append(defs, def{Name: s.Name, Kind: s.Kind, Line: s.Line})
			}
			break
		}
		if len(defs) > 0 {
			sortDefs(defs)
			return defs, true
		}
	}
	return nil, false
}

// referencesFor counts uses of d, preferring the language server and
// falling back to the repository map's identifier count.
func referencesFor(ctx context.Context, in Input, rel string, d def) (refs int, files []string, partial bool) {
	if in.LSP != nil && ctx.Err() == nil {
		locs, err := in.LSP.References(ctx, rel, lsp.Point{Line: d.Line})
		if err == nil {
			seen := map[string]bool{}
			for _, l := range locs {
				if l.Path == rel && l.Line == d.Line {
					continue // the declaration itself
				}
				refs++
				seen[l.Path] = true
			}
			return refs, sortedKeys(seen), false
		}
	}
	if in.RepoIndex != nil {
		seen := map[string]bool{}
		if radius, ok := in.RepoIndex.BlastRadius(d.Name, false); ok {
			total := 0.0
			for _, u := range radius.Referencers {
				if u.Path == rel {
					continue // counted exactly below
				}
				total += u.Weight
				seen[u.Path] = true
			}
			refs = int(math.Round(total))
		}
		// The repository map links files to each other, so a symbol used
		// only inside the file that defines it read as unused — the case
		// that most needs reviewing looked the safest. Those uses are
		// counted from the planned content itself.
		if n := sameFileUses(in.New, d.Name, d.Line); n > 0 {
			refs += n
			seen[rel] = true
		}
		return refs, sortedKeys(seen), true
	}
	return 0, nil, false
}

// sameFileUses counts whole-word occurrences of name in content outside
// its definition line.
func sameFileUses(content, name string, defLine int) int {
	if name == "" {
		return 0
	}
	n := 0
	for i, line := range strings.Split(content, "\n") {
		if i+1 == defLine {
			continue
		}
		for rest := line; ; {
			at := strings.Index(rest, name)
			if at < 0 {
				break
			}
			end := at + len(name)
			if (at == 0 || !isIdent(rest[at-1])) && (end == len(rest) || !isIdent(rest[end])) {
				n++
			}
			rest = rest[end:]
		}
	}
	return n
}

func isIdent(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// intersecting returns the defs (in source order) whose approximated
// range overlaps [fromLine, toLine], both 1-based and inclusive. defs must
// already be sorted by Line.
func intersecting(defs []def, fromLine, toLine int) []def {
	var out []def
	for i, d := range defs {
		end := math.MaxInt32
		if i+1 < len(defs) {
			end = defs[i+1].Line - 1
		}
		if d.Line <= toLine && end >= fromLine {
			out = append(out, d)
		}
	}
	return out
}

func sortDefs(defs []def) {
	sort.SliceStable(defs, func(i, j int) bool { return defs[i].Line < defs[j].Line })
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// changedRange finds the span of lines that differ between before and
// after, as 1-based inclusive line numbers into before. ok is false when
// the two are identical. A pure insertion (nothing removed) reports the
// single line it was inserted after, clamped to the first line.
func changedRange(before, after string) (fromLine, toLine int, ok bool) {
	a := strings.Split(before, "\n")
	b := strings.Split(after, "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	if i == len(a) && i == len(b) {
		return 0, 0, false
	}
	j, k := len(a)-1, len(b)-1
	for j >= i && k >= i && a[j] == b[k] {
		j, k = j-1, k-1
	}
	if j < i {
		j = i
	}
	return i + 1, j + 1, true
}

// relPath makes path comparable with the slash-relative paths LSP and
// repomap results carry.
func relPath(cwd, path string) string {
	if cwd != "" && filepath.IsAbs(path) {
		if r, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(r, "..") {
			path = r
		}
	}
	return filepath.ToSlash(path)
}

// siblingTestFile names the file a test for rel would conventionally live
// in, when that file exists. rel itself is never offered as its own
// sibling.
func siblingTestFile(cwd, rel string) string {
	if tool.IsTestFile(rel) {
		return ""
	}
	ext := filepath.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	dir, name := filepath.Split(base)
	var candidates []string
	switch ext {
	case ".go":
		candidates = []string{base + "_test.go"}
	case ".py":
		candidates = []string{dir + "test_" + name + ext, base + "_test" + ext}
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		candidates = []string{base + ".test" + ext, base + ".spec" + ext}
	default:
		candidates = []string{base + "_test" + ext}
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(c))); err == nil {
			return c
		}
	}
	return ""
}
