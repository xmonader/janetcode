package undoabout

import (
	"fmt"
	"github.com/xmonader/janetcode/internal/change"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// codeLexer yields the identifiers on source lines that are code: outside
// comments and string literals, and not numbers. It is a lexer for a
// language family, not a parser for any one language — enough that a word
// in a comment ("Mixed UTF-8 input"), an escape inside a string ("\x89")
// or a hex literal (0x89ab) is never taken for a reference to a name. A
// block comment, a raw string or a triple-quoted string carries over from
// one line to the next; feed it one file's (or one diff side's) lines in
// order.
type codeLexer struct {
	slashes bool // // line comments and /* */ block comments
	hash    bool // # line comments
	raw     bool // `...` strings, which may span lines
	triple  bool // """...""" and '''...''' strings, which may span lines
	inBlock bool
	inQuote string // the delimiter of a multi-line string still open
}

var slashFamily = map[string]bool{
	".go": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".java": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".rs": true,
	".swift": true, ".kt": true, ".kts": true, ".cs": true, ".scala": true, ".php": true, ".dart": true, ".zig": true,
}

var hashFamily = map[string]bool{
	".py": true, ".rb": true, ".sh": true, ".bash": true, ".zsh": true, ".pl": true, ".r": true,
	".yaml": true, ".yml": true, ".toml": true, ".ex": true, ".exs": true, ".nim": true, ".cr": true,
}

// lexerFor is a codeLexer for path's language family; an unknown one
// still has its quoted strings skipped.
func lexerFor(path string) *codeLexer {
	ext := strings.ToLower(filepath.Ext(path))
	return &codeLexer{
		slashes: slashFamily[ext],
		hash:    hashFamily[ext] || ext == ".php",
		raw:     ext == ".go" || ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" || ext == ".ts" || ext == ".tsx",
		triple:  ext == ".py",
	}
}

// hexLike matches what is left of an escape or a literal once its leading
// digit or backslash is gone: x89, xff00.
var hexLike = regexp.MustCompile(`^x[0-9A-Fa-f]+$`)

// idents returns the identifiers on one line, in order and with repeats.
func (lx *codeLexer) idents(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		switch {
		case lx.inBlock:
			end := strings.Index(line[i:], "*/")
			if end < 0 {
				return out
			}
			i, lx.inBlock = i+end+2, false
		case lx.inQuote != "":
			end := strings.Index(line[i:], lx.inQuote)
			if end < 0 {
				return out
			}
			i, lx.inQuote = i+end+len(lx.inQuote), ""
		case lx.slashes && strings.HasPrefix(line[i:], "//"), lx.hash && line[i] == '#':
			return out
		case lx.slashes && strings.HasPrefix(line[i:], "/*"):
			i, lx.inBlock = i+2, true
		case lx.triple && (strings.HasPrefix(line[i:], `"""`) || strings.HasPrefix(line[i:], "'''")):
			lx.inQuote = line[i : i+3]
			i += 3
		case lx.raw && line[i] == '`':
			lx.inQuote = "`"
			i++
		case line[i] == '"' || line[i] == '\'':
			// An ordinary string ends on its line; one left open (a Rust
			// lifetime, an apostrophe in prose) ends with the line too.
			q := line[i]
			j := i + 1
			for j < len(line) && line[j] != q {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case line[i] == '_' || line[i] < 0x80 && unicode.IsLetter(rune(line[i])):
			j := i
			for j < len(line) && (line[j] == '_' || line[j] < 0x80 && (unicode.IsLetter(rune(line[j])) || unicode.IsDigit(rune(line[j])))) {
				j++
			}
			if tok := line[i:j]; !hexLike.MatchString(tok) {
				out = append(out, tok)
			}
			i = j
		case line[i] >= '0' && line[i] <= '9':
			// A number, hex or not, with any suffix: 0x89ab, 1e9, 10ms.
			j := i
			for j < len(line) && (line[j] == '_' || line[j] == '.' || line[j] < 0x80 && (unicode.IsLetter(rune(line[j])) || unicode.IsDigit(rune(line[j])))) {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return out
}

// hunkIdents is every identifier on c's lines with the given diff prefix
// ('+' or '-'), with repeats, read as code: the side of the diff is lexed
// with its context lines so a comment opened on one line is still a
// comment on the next.
func hunkIdents(c change.Change, prefix byte) []string {
	lx := lexerFor(c.Path)
	var out []string
	for _, h := range c.Hunks() {
		for _, l := range h.Lines {
			if len(l) == 0 || l[0] != prefix && l[0] != ' ' {
				continue
			}
			ids := lx.idents(l[1:])
			if l[0] == prefix {
				out = append(out, ids...)
			}
		}
	}
	return out
}

// identStopwords excludes the handful of keywords, builtins and generic
// words common enough, across Go and C-ish languages, that flagging them
// as a coupled identifier would be mostly noise: control flow, primitive
// type names, builtin functions and a few words that show up in nearly
// every hunk regardless of what it does. It is deliberately small —
// anything not obviously a keyword is left in, since a false warning
// costs one line of output and a false "no coupling" costs a broken
// build.
var identStopwords = map[string]bool{
	"func": true, "package": true, "import": true, "return": true,
	"if": true, "else": true, "for": true, "range": true, "switch": true,
	"case": true, "default": true, "break": true, "continue": true,
	"defer": true, "chan": true, "struct": true, "interface": true,
	"type": true, "const": true, "var": true, "nil": true, "true": true,
	"false": true, "iota": true, "select": true, "fallthrough": true,
	"goto": true, "string": true, "int": true, "int32": true, "int64": true,
	"bool": true, "byte": true, "rune": true, "error": true, "uint": true,
	"void": true, "char": true, "long": true, "short": true,
	"double": true, "static": true, "public": true, "private": true,
	"protected": true, "class": true, "new": true, "this": true,
	"null": true, "include": true, "define": true, "namespace": true,
	"using": true, "template": true, "virtual": true, "override": true,
	"and": true, "with": true, "from": true, "the": true,
	"len": true, "cap": true, "append": true, "make": true, "copy": true,
	"delete": true, "panic": true, "recover": true, "print": true,
	"println": true, "close": true, "min": true, "max": true, "clear": true,
	"any": true, "float32": true, "float64": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "int8": true, "int16": true,
}

// addedIdents is the identifiers c genuinely introduces: those appearing
// in the code of any of its added ('+') lines, across all its hunks, that
// do not also appear on any of its removed ('-') lines anywhere in the
// change. The exclusion is what keeps a line rewritten for an unrelated
// reason from counting as "introducing" every identifier that line happens
// to carry — a hunk that changes a log message's wording carries its
// call, say `log.Println(...)`, on both the removed and the added side
// unchanged; only the wording itself is new, and being inside a string it
// is not code at all. Unlike a hunk-by-hunk rename detector, this is
// computed across the whole change, so a rename split across widely
// separated hunks (a definition here, a call site there) is caught
// exactly as well as one two lines apart in the same hunk as an unrelated
// edit.
func addedIdents(c change.Change) map[string]bool {
	removed := map[string]bool{}
	for _, t := range hunkIdents(c, '-') {
		removed[t] = true
	}
	out := map[string]bool{}
	for _, t := range hunkIdents(c, '+') {
		if !removed[t] {
			out[t] = true
		}
	}
	return out
}

// meaningfulIdents narrows a set of identifiers to the ones worth checking
// for coupling: real words, not a keyword or builtin.
func meaningfulIdents(ids map[string]bool) map[string]bool {
	out := map[string]bool{}
	for t := range ids {
		if len(t) < 3 || identStopwords[t] {
			continue
		}
		out[t] = true
	}
	return out
}

// countIdent counts occurrences of tok in the code of one file's lines.
func countIdent(path string, lines []string, tok string) int {
	lx := lexerFor(path)
	n := 0
	for _, l := range lines {
		for _, t := range lx.idents(l) {
			if t == tok {
				n++
			}
		}
	}
	return n
}

// canReference reports whether code in kept can refer to tok defined by
// undone: always within one directory (one package, for Go), and across
// directories only for an exported-looking name — a lowercase name in
// another package's file is that file's own, whatever it is called.
func canReference(kept, undone change.Change, tok string) bool {
	if filepath.Dir(kept.Path) == filepath.Dir(undone.Path) {
		return true
	}
	r := []rune(tok)
	return len(r) > 0 && unicode.IsUpper(r[0])
}

// simulateUndoneContent computes, for every file some change in undo
// touches, that file's content once undo is reverted — current disk
// content with each of undo's changes to that file applied in reverse,
// newest first, exactly the order and per-hunk location logic
// UndoSelection itself uses (see revertContent, UndoChange's own
// implementation). A path is left out of the result when its content
// cannot be read, or a change on it cannot be relocated (which should not
// happen for a selection built from SelectChanges, since partitionUndoable
// already excluded any change like that from the candidates); the caller
// falls back to disk content for a path with no entry.
func simulateUndoneContent(undo []change.Change) map[string][]string {
	byPath := map[string][]change.Change{}
	for _, c := range undo {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	out := map[string][]string{}
	for path, chs := range byPath {
		sort.Slice(chs, func(i, j int) bool { return chs[i].Seq > chs[j].Seq }) // newest first
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := diff.SplitLinesKeepEOL(string(data))
		ok := true
		for _, c := range chs {
			if !c.Existed {
				// This change created the file; reverting it removes it,
				// so there is nothing left for an older change (rare: a
				// second change to a path whose first one created it) to
				// apply to.
				lines, ok = nil, false
				break
			}
			reverted, rok, _ := change.Revert(lines, c)
			if !rok {
				ok = false
				break
			}
			lines = reverted
		}
		if ok {
			out[path] = lines
		}
	}
	return out
}

// detectCoupling is undo selection's guard independent of the model's own
// judgement, and independent of recognizing any particular edit shape
// (rename, deleted helper, dropped import, ...): it simulates reverting
// sel.Undo and asks, for every meaningful identifier a kept change relies
// on (appears on its own added lines) that some undone change also
// introduced (appears on its added lines too), whether the simulated
// result still has that identifier anywhere in the touched files besides
// the kept change's own added lines. If not, keeping that change while
// undoing the other would very likely leave it referring to something
// that no longer exists. Each such pairing is reported once, in a stable
// order.
func detectCoupling(cwd string, sel Selection) []string {
	if len(sel.Undo) == 0 || len(sel.Keep) == 0 {
		return nil
	}
	touched := TouchedPaths(sel)
	simulated := simulateUndoneContent(sel.Undo)
	content := make(map[string][]string, len(touched))
	for _, p := range touched {
		if lines, ok := simulated[p]; ok {
			content[p] = lines
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			content[p] = diff.SplitLinesKeepEOL(string(data))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range sel.Keep {
		kAdded := addedIdents(k)
		for t := range meaningfulIdents(kAdded) {
			var coupledWith *change.Change
			for i := range sel.Undo {
				if addedIdents(sel.Undo[i])[t] && canReference(k, sel.Undo[i], t) {
					coupledWith = &sel.Undo[i]
					break
				}
			}
			if coupledWith == nil {
				continue
			}
			total := 0
			for _, p := range touched {
				total += countIdent(p, content[p], t)
			}
			inKept := 0
			for _, id := range hunkIdents(k, '+') {
				if id == t {
					inKept++
				}
			}
			if total-inKept > 0 {
				continue // t still occurs somewhere else; nothing orphaned
			}
			key := fmt.Sprintf("%d|%d|%s", coupledWith.Seq, k.Seq, t)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fmt.Sprintf("#%d uses %s, which undoing #%d would remove from %s; the result would likely not build — undo both, or keep both", k.Seq, t, coupledWith.Seq, textfmt.RelTo(cwd, coupledWith.Path)))
		}
	}
	sort.Strings(out)
	return out
}

// partitionUndoable splits candidates into those whose recorded edit can
// still be located against the file's current content and those that
// cannot — the file was edited, or reverted outside the session (e.g. by
// `git checkout`), since the change was made. Neither undoing nor keeping
// such a change is a real choice, so it must never reach the model or the
// keyword fallback as one; the caller reports it separately (see
// Selection.Unavailable).
//
// Each path's own candidates are walked newest to oldest, chaining each
// hypothetical revert into the next (using revertContent, the same
// function UndoChange itself reverts with): a change blocked only because
// a later, still-present change to the same file sits on top of it is not
// marked unavailable on that account, since a selection that undoes that
// later change too — UndoSelection always reverts newest first — would
// restore the state it needs. A change whose own edit cannot be found even
// so (typically the newest one on its file, if any) is unavailable no
// matter what else gets selected.
func partitionUndoable(cwd string, candidates []change.Change) (ok []change.Change, unavailable []UnavailableChange) {
	byPath := map[string][]change.Change{}
	for _, c := range candidates {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	bad := map[int]string{}
	for path, chs := range byPath {
		sort.Slice(chs, func(i, j int) bool { return chs[i].Seq > chs[j].Seq }) // newest first
		data, rerr := os.ReadFile(path)
		exists := rerr == nil
		var lines []string
		if exists {
			lines = diff.SplitLinesKeepEOL(string(data))
		}
		for _, c := range chs {
			if !c.Existed {
				// c created path: undoable iff the file, as this
				// hypothetical chain has it so far, still matches
				// exactly what c wrote.
				if exists && strings.Join(lines, "") == string(c.After) {
					exists, lines = false, nil
					continue
				}
				bad[c.Seq] = fmt.Sprintf("#%d created %s, which no longer matches what it wrote; undoing it would guess at what to remove", c.Seq, textfmt.RelTo(cwd, path))
				continue
			}
			if !exists {
				bad[c.Seq] = fmt.Sprintf("#%d edited %s, which is gone since", c.Seq, textfmt.RelTo(cwd, path))
				continue
			}
			reverted, rok, badHunk := change.Revert(lines, c)
			if !rok {
				h := c.Hunks()[badHunk-1]
				bad[c.Seq] = fmt.Sprintf("#%d's hunk %d (%s) of %s no longer applies: those lines were edited outside the session since", c.Seq, badHunk, h.Header(), textfmt.RelTo(cwd, path))
				continue
			}
			lines = reverted
		}
	}
	for _, c := range candidates {
		if why, isBad := bad[c.Seq]; isBad {
			unavailable = append(unavailable, UnavailableChange{Change: c, Why: why})
		} else {
			ok = append(ok, c)
		}
	}
	return ok, unavailable
}
