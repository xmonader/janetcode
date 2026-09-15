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

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// Symbols are the existing definitions the edit's changed lines fall
	// inside.
	Symbols []Symbol
	// Added are definitions the edit introduces: present in the new
	// content, absent from the old. Nothing references them yet, so they
	// carry no refs, and adding one is not a change to its neighbours.
	Added []Symbol
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
//	changes Parse (func) · 14 refs in 6 files · adds parseAll (func) · tests: parse_test.go, cli_test.go · checks: go test ./...
//
// It is "" when nothing is known: a non-code file, or an edit that
// touches no recognizable definition.
func (i Impact) Line() string {
	if len(i.Symbols) == 0 && len(i.Added) == 0 {
		return ""
	}
	var parts []string
	if len(i.Symbols) > 0 {
		totalRefs, totalFiles := 0, 0
		for _, s := range i.Symbols {
			totalRefs += s.Refs
			totalFiles += s.RefFiles
		}
		parts = append(parts, "changes "+labels(i.Symbols))
		parts = append(parts, fmt.Sprintf("%s in %s", textfmt.Plural(totalRefs, "ref"), textfmt.Plural(totalFiles, "file")))
	}
	if len(i.Added) > 0 {
		parts = append(parts, "adds "+labels(i.Added))
	}
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

// labels names symbols with their kinds, "Parse (func), Token (type)".
func labels(syms []Symbol) string {
	names := make([]string, len(syms))
	for j, s := range syms {
		names[j] = s.Name
		if s.Kind != "" {
			names[j] += " (" + s.Kind + ")"
		}
	}
	return strings.Join(names, ", ")
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
// approximates one (see reach).
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
	removed, inserted := changedLines(in.Old, in.New)
	if len(removed) == 0 && len(inserted) == 0 {
		return out
	}
	rel := relPath(in.Cwd, in.Path)

	defs, partial := fileDefs(ctx, in, rel)
	touched, added := touchedDefs(in, rel, defs, removed, inserted)
	for _, d := range added {
		out.Added = append(out.Added, Symbol{Name: d.Name, Kind: d.Kind})
	}
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
			if project.IsTestFile(f) {
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
			if (at == 0 || !repomap.IsIdentByte(rest[at-1])) && (end == len(rest) || !repomap.IsIdentByte(rest[end])) {
				n++
			}
			rest = rest[end:]
		}
	}
	return n
}

// touchedDefs sorts what an edit's changed lines reach into existing
// definitions it changes and definitions it adds. A line the edit removes
// or replaces is looked up among defs, the source's definitions of the
// old content. A line it inserts has no place in the old content, so it
// is looked up among the new content's own definitions (parsed here, the
// way the repository map parses a file): one whose name the old content
// also defines is a change to that definition, reported as defs has it;
// one it does not is added.
func touchedDefs(in Input, rel string, defs []def, removed, inserted []int) (touched, added []def) {
	oldLines := strings.Split(in.Old, "\n")
	seen := map[string]bool{}
	take := func(d def) {
		if !seen[d.Name] {
			seen[d.Name] = true
			touched = append(touched, d)
		}
	}
	for _, d := range reaching(defs, oldLines, removed) {
		take(d)
	}
	if len(inserted) == 0 {
		return touched, nil
	}
	// The old content is parsed the same way as the new, so names compare
	// like for like whatever naming the source's own defs use.
	oldAt := map[string]int{}
	sym := in.RepoIndex.Parser()
	for _, d := range parsedDefs(rel, in.Old, sym) {
		oldAt[d.Name] = d.Line
	}
	for _, d := range reaching(parsedDefs(rel, in.New, sym), strings.Split(in.New, "\n"), inserted) {
		line, existed := oldAt[d.Name]
		if !existed {
			added = append(added, d)
			continue
		}
		take(sourceDef(defs, oldLines, d, line))
	}
	sortDefs(touched)
	return touched, added
}

// sourceDef is the definition in defs that the parsed definition d, at
// line of the old content, corresponds to: the one whose reach holds that
// line, or d itself (at its old line) when the source has none there.
func sourceDef(defs []def, oldLines []string, d def, line int) def {
	if found := reaching(defs, oldLines, []int{line}); len(found) > 0 {
		return found[0]
	}
	d.Line = line
	return d
}

// parsedDefs is content's definitions as the repository map reads them.
func parsedDefs(rel, content string, sym repomap.Parser) []def {
	var defs []def
	for _, s := range repomap.Defs(rel, []byte(content), sym) {
		defs = append(defs, def{Name: s.Name, Kind: s.Kind, Line: s.Line})
	}
	sortDefs(defs)
	return defs
}

// reaching returns the defs (in source order) whose reach holds any of
// lines, 1-based line numbers into content. A blank line reaches nothing,
// so the blank line separating an added function from its neighbour does
// not make the neighbour changed. defs must be sorted by Line.
func reaching(defs []def, content []string, lines []int) []def {
	var out []def
	for i := range defs {
		start, end := reach(defs, content, i)
		for _, l := range lines {
			if l >= start && l <= end && l <= len(content) && strings.TrimSpace(content[l-1]) != "" {
				out = append(out, defs[i])
				break
			}
		}
	}
	return out
}

// reach approximates the lines defs[i] spans in content: from its own
// line to the line before the next definition (or the end of the file
// for the last one), less the blank and comment lines directly above that
// next definition — its doc comment, not this definition's body. Without
// that trim a function inserted with its doc comment right after another
// read as a change to the one before it.
func reach(defs []def, content []string, i int) (start, end int) {
	start, end = defs[i].Line, len(content)
	if i+1 < len(defs) {
		end = defs[i+1].Line - 1
		for end > start && end <= len(content) && (strings.TrimSpace(content[end-1]) == "" || commentLine(content[end-1])) {
			end--
		}
	}
	return start, end
}

// commentLine reports whether line holds only a comment, in the line
// comment syntaxes of the languages the repository map indexes.
func commentLine(line string) bool {
	t := strings.TrimSpace(line)
	for _, p := range []string{"//", "#", "/*", "*", "--"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
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

// changedLines diffs before and after line by line. removed holds the
// 1-based lines of before that the edit deletes or replaces; inserted
// holds the 1-based lines of after that it adds. A pure insertion reports
// only the lines inserted, not a span of the old file around them, and
// two separate changes (an import at the top, a function at the bottom)
// report their own lines rather than everything between them.
func changedLines(before, after string) (removed, inserted []int) {
	ops := diff.Lines(strings.Split(before, "\n"), strings.Split(after, "\n"))
	slideToBlank(ops)
	oldLine, newLine := 0, 0
	for _, op := range ops {
		switch op.Kind {
		case '-':
			oldLine++
			removed = append(removed, oldLine)
		case '+':
			newLine++
			inserted = append(inserted, newLine)
		default:
			oldLine++
			newLine++
		}
	}
	return removed, inserted
}

// slideToBlank moves each run of only-inserted or only-removed lines to
// the position, among the equally minimal diffs, where it ends on a blank
// line. A diff is free to show a function inserted after another as
// starting at the other's closing brace ("}", "", "// N…", "func N…")
// instead of at its own doc comment, and that would mark the brace — so
// the function before it — as changed.
func slideToBlank(ops []diff.Op) {
	for s := 0; s < len(ops); {
		if ops[s].Kind == ' ' {
			s++
			continue
		}
		e := s
		for e < len(ops) && ops[e].Kind == ops[s].Kind {
			e++
		}
		if (s > 0 && ops[s-1].Kind != ' ') || (e < len(ops) && ops[e].Kind != ' ') {
			s = e // part of a replacement, not a pure run
			continue
		}
		// Every position the run can slide down to shows the same change.
		best := -1
		for shift := 0; ; shift++ {
			if strings.TrimSpace(ops[e+shift-1].Text) == "" {
				best = shift
				break
			}
			if e+shift >= len(ops) || ops[e+shift].Kind != ' ' || ops[e+shift].Text != ops[s+shift].Text {
				break
			}
		}
		if best > 0 {
			kind := ops[s].Kind
			for k := 0; k < best; k++ {
				ops[s+k].Kind, ops[e+k].Kind = ' ', kind
			}
		}
		s = e + max(best, 0)
	}
}

// relPath makes path comparable with the slash-relative paths LSP and
// repomap results carry.
func relPath(cwd, path string) string {
	return filepath.ToSlash(textfmt.RelTo(cwd, path))
}

// siblingTestFile names the file a test for rel would conventionally live
// in, when that file exists. rel itself is never offered as its own
// sibling.
func siblingTestFile(cwd, rel string) string {
	if project.IsTestFile(rel) {
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
