package agent

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// identToken matches a bare identifier: letters, digits and underscore,
// not starting with a digit. Good enough to spot a renamed symbol without
// parsing any particular language.
var identToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// tokensWithPrefix collects the identifiers on a hunk's lines that carry
// the given diff prefix ('+' or '-'), ignoring the prefix byte itself.
func tokensWithPrefix(lines []string, prefix byte) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		if len(l) == 0 || l[0] != prefix {
			continue
		}
		for _, t := range identToken.FindAllString(l[1:], -1) {
			out[t] = true
		}
	}
	return out
}

// identStopwords excludes the handful of keywords and generic words common
// enough, across Go and C-ish languages, that flagging them as a coupled
// identifier would be mostly noise: control flow, primitive type names and
// a few words that show up in nearly every hunk regardless of what it
// does. It is deliberately small — anything not obviously a keyword is
// left in, since a false warning costs one line of output and a false "no
// coupling" costs a broken build.
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
}

// addedIdents is the identifiers c genuinely introduces: those appearing
// on any of its added ('+') lines, across all its hunks, that do not also
// appear on any of its removed ('-') lines anywhere in the change. The
// exclusion is what keeps a line rewritten for an unrelated reason from
// counting as "introducing" every identifier that line happens to carry —
// a hunk that changes a log message's wording carries its call, say
// `log.Println(...)`, on both the removed and the added side unchanged;
// only the wording itself is new. Unlike a hunk-by-hunk rename detector,
// this is computed across the whole change, so a rename split across
// widely separated hunks (a definition here, a call site there) is caught
// exactly as well as one two lines apart in the same hunk as an unrelated
// edit.
func addedIdents(c Change) map[string]bool {
	added, removed := map[string]bool{}, map[string]bool{}
	for _, h := range c.Hunks() {
		for t := range tokensWithPrefix(h.Lines, '+') {
			added[t] = true
		}
		for t := range tokensWithPrefix(h.Lines, '-') {
			removed[t] = true
		}
	}
	out := map[string]bool{}
	for t := range added {
		if !removed[t] {
			out[t] = true
		}
	}
	return out
}

// meaningfulIdents narrows a set of identifiers to the ones worth checking
// for coupling: real words, not a keyword (identToken never yields a
// purely numeric token).
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

// countIdent counts whole-word occurrences of tok across lines.
func countIdent(lines []string, tok string) int {
	n := 0
	for _, l := range lines {
		for _, t := range identToken.FindAllString(l, -1) {
			if t == tok {
				n++
			}
		}
	}
	return n
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
func simulateUndoneContent(undo []Change) map[string][]string {
	byPath := map[string][]Change{}
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
			reverted, rok, _ := revertContent(lines, c)
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
	touched := selectionTouchedPaths(sel)
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
			var coupledWith *Change
			for i := range sel.Undo {
				if addedIdents(sel.Undo[i])[t] {
					coupledWith = &sel.Undo[i]
					break
				}
			}
			if coupledWith == nil {
				continue
			}
			total := 0
			for _, p := range touched {
				total += countIdent(content[p], t)
			}
			inKept := countIdent(hunkAddedLines(k), t)
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

// hunkAddedLines is the literal text of c's added lines, prefix stripped,
// across all its hunks.
func hunkAddedLines(c Change) []string {
	var out []string
	for _, h := range c.Hunks() {
		for _, l := range h.Lines {
			if len(l) > 0 && l[0] == '+' {
				out = append(out, l[1:])
			}
		}
	}
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
func partitionUndoable(cwd string, candidates []Change) (ok []Change, unavailable []UnavailableChange) {
	byPath := map[string][]Change{}
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
			reverted, rok, badHunk := revertContent(lines, c)
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
