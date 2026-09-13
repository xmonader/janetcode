package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/verify"
)

// Selection is the answer to "which of this session's changes match a
// plain-language description": Undo is what would be reverted, Keep is
// everything else considered (so a preview can show both sides), Mixed is
// a change the model could not cleanly put on either side (see Mixed),
// Unavailable is a change excluded from consideration entirely because its
// own edit can no longer be located (see partitionUndoable), Why is one
// line explaining the split, Fallback marks a selection made by keyword
// overlap rather than a model, because none was configured or its reply
// could not be used, and Warnings is the deterministic coupling guard's
// findings (see detectCoupling) — independent of how the split was made.
type Selection struct {
	Undo        []Change
	Keep        []Change
	Mixed       []Mixed
	Unavailable []UnavailableChange
	Why         string
	Fallback    bool
	Warnings    []string
}

// Mixed is a recorded change the model said contains both edits that
// match the undo description and edits that do not — a rename and a
// logging tweak made in the same write_file or edit_file call, say. It is
// placed on neither side of the split: choosing one would be a guess
// about which half of the change the user meant, so ApplyPendingUndo
// refuses while any are present unless run with force.
type Mixed struct {
	Change Change
	Why    string
}

// UnavailableChange is a recorded change SelectChanges left out of the
// model's (or the keyword fallback's) candidates entirely: its own edit
// could no longer be located in the file as it is now, so neither undoing
// nor keeping it is a real choice — the file was edited, or reverted (e.g.
// by `git checkout`), outside the session since it was recorded. See
// partitionUndoable.
type UnavailableChange struct {
	Change Change
	Why    string
}

// UndoReport is what UndoSelection did.
type UndoReport struct {
	// Reverted is one line per change actually undone, newest first (the
	// order they were applied), in UndoChange's own wording.
	Reverted []string
	// Refused is set when a revert stopped partway: the error from the
	// first change that refused, plus which of the selection's changes
	// were never attempted because of it.
	Refused string
	// Checks is the verify gate's summary line, run once after any
	// change was reverted; empty when nothing was reverted, or nothing
	// ran to verify it (see CheckNote and Diagnostics for why).
	Checks string
	// ChecksOK is nil when Checks was not run, else whether it passed.
	ChecksOK *bool
	// CheckNote explains why Checks is empty even though something was
	// reverted: no verify commands are configured, or they are but are
	// not trusted. Empty whenever Checks ran (or nothing was reverted).
	CheckNote string
	// Diagnostics is the language-server fallback's report, consulted only
	// when CheckNote is set (no verify check ran): one line per file any
	// of the selection's candidates touched — reverted or kept, so a kept
	// change that only builds against something an undone one just
	// removed is not missed — naming the file and either what the
	// language server found or that the file could not be checked, or a
	// plain admission that nothing verified the result when no diagnose
	// hook is available at all. Empty whenever Checks ran.
	Diagnostics string
}

// ErrNoChangesToUndo is returned by SelectChanges when this session has no
// change left that a description could match (everything is already
// undone, or nothing was ever changed).
var ErrNoChangesToUndo = errors.New("agent: no changes to undo in this session")

// ErrNothingSelected is returned by UndoSelection when the selection's
// Undo side is empty: there is nothing to revert.
var ErrNothingSelected = errors.New("agent: nothing selected to undo")

// undoCandidates are this session's changes eligible for description
// matching: still in effect, and not themselves the record of an earlier
// undo (reverting a revert is not what "undo about X" means).
func (a *Agent) undoCandidates() []Change {
	var out []Change
	for _, c := range a.Changes() {
		if c.Undone || c.Tool == "undo" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// SelectChanges matches description against this session's undoable
// changes: with a provider configured, one side model call (not added to
// the conversation, the same discipline as AskOnce) is shown a compact
// numbered list of the candidates and asked which to undo and which to
// keep; without a provider, or when the model's reply cannot be used, a
// BM25 keyword ranking over each candidate's intent, path and diff takes
// over and Selection.Fallback is set so the caller can say so.
func (a *Agent) SelectChanges(ctx context.Context, description string) (Selection, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return Selection{}, fmt.Errorf("empty description")
	}
	all := a.undoCandidates()
	if len(all) == 0 {
		return Selection{}, ErrNoChangesToUndo
	}
	// A candidate whose own edit can no longer be located is not a choice
	// between undo and keep at all — offering it either way would promise
	// something this guard cannot actually do. It is excluded before the
	// model or the keyword fallback ever sees it (see partitionUndoable).
	candidates, unavailable := partitionUndoable(all)
	if len(candidates) == 0 {
		// Every change is still there but none can be reverted any more:
		// say which and why, rather than claiming there is nothing.
		return Selection{Unavailable: unavailable, Why: "every change left in this session was edited or reverted since"}, nil
	}
	sel, got := Selection{}, false
	if a.Provider != nil {
		if s, err := a.selectByModel(ctx, description, candidates); err == nil {
			sel, got = s, true
		}
	}
	if !got {
		sel = selectByKeywords(description, candidates)
	}
	sel.Unavailable = unavailable
	// The coupling guard runs regardless of how the split was made: a
	// model can miss it as easily as the keyword fallback can.
	sel.Warnings = detectCoupling(sel)
	return sel, nil
}

const selectSystem = "You are selecting which of a coding session's file changes match a plain-language description, so the matching ones can be undone while the rest stay untouched. You are given the description and a numbered list of changes: each change's path, the model's stated reason for making it, how many lines it added and removed, and a few of the changed lines. Undo a change only when it clearly matches the description; when a change is unrelated, or you are not sure, keep it. One logical change can span several recorded changes across different files or calls — a rename touching both a definition and its call sites, for instance — treat those as a unit and put them all on the same side; never split one logical change across undo and keep. When a single recorded change itself mixes edits that match the description with edits that do not, do not guess which half to keep: list its number in \"mixed\" with why, instead of placing it in \"undo\" or \"keep\". Reply with JSON only, no prose outside it."

// undoSelectSchema constrains the model's reply the same way compaction's
// working-memory record does (see working.go): a provider that supports
// it is guaranteed to answer in this shape, one that does not is asked
// anyway and ignores the field, and the reply is parsed the same way
// either way.
var undoSelectSchema = &llm.Schema{
	Name:   "undo_selection",
	Strict: true,
	JSON: json.RawMessage(`{
"type":"object",
"properties":{
"undo":{"type":"array","items":{"type":"integer"}},
"keep":{"type":"array","items":{"type":"integer"}},
"mixed":{"type":"array","items":{"type":"object","properties":{"seq":{"type":"integer"},"why":{"type":"string"}},"required":["seq","why"],"additionalProperties":false}},
"why":{"type":"string"}},
"required":["undo","keep","mixed","why"],
"additionalProperties":false}`),
}

const (
	selectMaxHunksPerChange = 3
	selectMaxLinesPerHunk   = 4
	selectMaxPromptBytes    = 12_000
)

// selectByModel asks the model to split candidates against description.
func (a *Agent) selectByModel(ctx context.Context, description string, candidates []Change) (Selection, error) {
	prompt := buildSelectPrompt(processCwd(), description, candidates)
	resp, err := a.Provider.Stream(ctx, llm.Request{
		Model:     a.Model,
		System:    selectSystem,
		Messages:  []llm.Message{llm.TextMessage(llm.RoleUser, prompt)},
		MaxTokens: 1024,
		Schema:    undoSelectSchema,
	}, nil)
	if err != nil {
		return Selection{}, err
	}
	if resp == nil {
		return Selection{}, fmt.Errorf("empty reply")
	}
	undoIDs, keepIDs, mixedIDs, why, err := parseSelectionReply(resp.Message.Text())
	if err != nil {
		return Selection{}, err
	}
	return resolveSelection(candidates, undoIDs, keepIDs, mixedIDs, why), nil
}

// buildSelectPrompt renders description and candidates for selectByModel,
// capped so a long session cannot blow the call's budget: each change
// shows a few hunks, each hunk a few of its changed (not context) lines,
// and the whole prompt stops growing past selectMaxPromptBytes.
func buildSelectPrompt(cwd, description string, candidates []Change) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Description: %s\n\nCandidates:\n", description)
	for _, c := range candidates {
		add, del := c.Stats()
		fmt.Fprintf(&sb, "\n#%d turn %d %s +%d -%d", c.Seq, c.Turn, relTo(cwd, c.Path), add, del)
		if c.Intent != "" {
			fmt.Fprintf(&sb, " · %s", c.Intent)
		}
		sb.WriteString("\n")
		hunks := c.Hunks()
		for i, h := range hunks {
			if i >= selectMaxHunksPerChange {
				fmt.Fprintf(&sb, "  … %d more hunk(s)\n", len(hunks)-i)
				break
			}
			sb.WriteString("  " + h.Header() + "\n")
			shown := 0
			for _, l := range h.Lines {
				if l[0] == ' ' {
					continue
				}
				if shown >= selectMaxLinesPerHunk {
					sb.WriteString("    …\n")
					break
				}
				sb.WriteString("  " + strings.TrimRight(l, "\n") + "\n")
				shown++
			}
		}
		if sb.Len() > selectMaxPromptBytes {
			sb.WriteString("\n[more candidates omitted for size]\n")
			break
		}
	}
	sb.WriteString("\nReply with JSON {\"undo\":[...],\"keep\":[...],\"mixed\":[{\"seq\":n,\"why\":\"...\"}],\"why\":\"...\"} using the change numbers above. Keep a logical change that spans several recorded changes together on one side; list a change that mixes matching and unrelated edits in \"mixed\" instead of guessing which half to keep.")
	return sb.String()
}

// mixedSeq is one element of the model's "mixed" list: a change it could
// not cleanly place, and why.
type mixedSeq struct {
	Seq int    `json:"seq"`
	Why string `json:"why"`
}

// parseSelectionReply reads the model's JSON, tolerating a reply wrapped
// in a code fence or followed by trailing prose: the same "first { to
// last }" extraction working.go's merge uses for the compaction record.
func parseSelectionReply(text string) (undo, keep []int, mixed []mixedSeq, why string, err error) {
	text = strings.TrimSpace(text)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, nil, nil, "", fmt.Errorf("no JSON object in the model's reply")
	}
	var got struct {
		Undo  []int      `json:"undo"`
		Keep  []int      `json:"keep"`
		Mixed []mixedSeq `json:"mixed"`
		Why   string     `json:"why"`
	}
	if jerr := json.Unmarshal([]byte(text[start:end+1]), &got); jerr != nil {
		return nil, nil, nil, "", jerr
	}
	return got.Undo, got.Keep, got.Mixed, strings.TrimSpace(got.Why), nil
}

// resolveSelection turns the model's (or the fallback's) seq numbers into
// Changes, silently dropping any seq that is not among candidates. A
// candidate the model named in none of the three lists is kept: undo is
// opt-in, not opt-out, so a name the model forgot to place does not get
// reverted.
func resolveSelection(candidates []Change, undoIDs, keepIDs []int, mixedIDs []mixedSeq, why string) Selection {
	byID := make(map[int]Change, len(candidates))
	for _, c := range candidates {
		byID[c.Seq] = c
	}
	var sel Selection
	placed := map[int]bool{}
	for _, id := range undoIDs {
		if c, ok := byID[id]; ok && !placed[id] {
			sel.Undo = append(sel.Undo, c)
			placed[id] = true
		}
	}
	for _, id := range keepIDs {
		if c, ok := byID[id]; ok && !placed[id] {
			sel.Keep = append(sel.Keep, c)
			placed[id] = true
		}
	}
	for _, m := range mixedIDs {
		if c, ok := byID[m.Seq]; ok && !placed[m.Seq] {
			sel.Mixed = append(sel.Mixed, Mixed{Change: c, Why: m.Why})
			placed[m.Seq] = true
		}
	}
	for _, c := range candidates {
		if !placed[c.Seq] {
			sel.Keep = append(sel.Keep, c)
		}
	}
	sel.Why = why
	return sel
}

// selectByKeywords is the fallback of last resort, used without a
// provider or when the model's reply could not be parsed: candidates are
// ranked against description by the same BM25 scoring searchArchive uses
// (see search.go), over each candidate's intent, path and the non-context
// lines of its diff. Keyword overlap is a much weaker signal than a model
// reading the description and the diff together, so a candidate only
// clears the bar when it scores close to the best match; everything else,
// including a candidate with no overlap at all, is kept.
func selectByKeywords(description string, candidates []Change) Selection {
	sel := Selection{Fallback: true}
	q := terms(description)
	if len(q) == 0 {
		sel.Keep = append(sel.Keep, candidates...)
		sel.Why = "the description had no searchable words; nothing was matched"
		return sel
	}
	docs := make([]map[string]int, len(candidates))
	lens := make([]int, len(candidates))
	df := map[string]int{}
	total := 0
	for i, c := range candidates {
		tf := map[string]int{}
		n := 0
		for _, t := range terms(changeText(c)) {
			tf[t]++
			n++
		}
		docs[i], lens[i] = tf, n
		total += n
		for t := range tf {
			df[t]++
		}
	}
	avg := math.Max(float64(total)/math.Max(float64(len(candidates)), 1), 1)
	scores := make([]float64, len(candidates))
	best := 0.0
	for i := range candidates {
		var score float64
		for _, t := range q {
			f := float64(docs[i][t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(candidates))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			score += idf * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*float64(lens[i])/avg))
		}
		scores[i] = score
		if score > best {
			best = score
		}
	}
	if best <= 0 {
		sel.Keep = append(sel.Keep, candidates...)
		sel.Why = "no session change's path, reason or diff shares a word with the description; nothing was matched"
		return sel
	}
	const relThreshold = 0.6 // a candidate must score within 40% of the best to be undone
	for i, c := range candidates {
		if scores[i] >= best*relThreshold {
			sel.Undo = append(sel.Undo, c)
		} else {
			sel.Keep = append(sel.Keep, c)
		}
	}
	sel.Why = "matched by keyword overlap with the description (no model available to judge meaning)"
	return sel
}

// changeText is what selectByKeywords scores a change against: its
// stated reason, its path, and the lines its diff actually changed
// (context lines would only dilute the match).
func changeText(c Change) string {
	var sb strings.Builder
	sb.WriteString(c.Intent)
	sb.WriteString(" ")
	sb.WriteString(c.Path)
	for _, h := range c.Hunks() {
		for _, l := range h.Lines {
			if l[0] != ' ' {
				sb.WriteString(l)
			}
		}
	}
	return sb.String()
}

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

// isAllDigits reports whether s is nothing but digits, so a stray line
// number or literal never counts as a "meaningful identifier" below (it
// is already unreachable via identToken for a token that starts with a
// digit; this catches an underscore-led one like "_42").
func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
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
// for coupling: real words, not a keyword, not purely numeric.
func meaningfulIdents(ids map[string]bool) map[string]bool {
	out := map[string]bool{}
	for t := range ids {
		if len(t) < 3 || identStopwords[t] || isAllDigits(t) {
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
		lines := splitLines(string(data))
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
func detectCoupling(sel Selection) []string {
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
			content[p] = splitLines(string(data))
		}
	}
	cwd := processCwd()
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
			out = append(out, fmt.Sprintf("#%d uses %s, which undoing #%d would remove from %s; the result would likely not build — undo both, or keep both", k.Seq, t, coupledWith.Seq, relTo(cwd, coupledWith.Path)))
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

// selectionTouchedPaths lists, without duplicates, every file any change
// sel considered touches: undone, kept, or left mixed.
func selectionTouchedPaths(sel Selection) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range sel.Undo {
		add(c.Path)
	}
	for _, c := range sel.Keep {
		add(c.Path)
	}
	for _, m := range sel.Mixed {
		add(m.Change.Path)
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
func partitionUndoable(candidates []Change) (ok []Change, unavailable []UnavailableChange) {
	byPath := map[string][]Change{}
	for _, c := range candidates {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	cwd := processCwd()
	bad := map[int]string{}
	for path, chs := range byPath {
		sort.Slice(chs, func(i, j int) bool { return chs[i].Seq > chs[j].Seq }) // newest first
		data, rerr := os.ReadFile(path)
		exists := rerr == nil
		var lines []string
		if exists {
			lines = splitLines(string(data))
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
				bad[c.Seq] = fmt.Sprintf("#%d created %s, which no longer matches what it wrote; undoing it would guess at what to remove", c.Seq, relTo(cwd, path))
				continue
			}
			if !exists {
				bad[c.Seq] = fmt.Sprintf("#%d edited %s, which is gone since", c.Seq, relTo(cwd, path))
				continue
			}
			reverted, rok, badHunk := revertContent(lines, c)
			if !rok {
				h := c.Hunks()[badHunk-1]
				bad[c.Seq] = fmt.Sprintf("#%d's hunk %d (%s) of %s no longer applies: those lines were edited outside the session since", c.Seq, badHunk, h.Header(), relTo(cwd, path))
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

// UndoSelection reverts sel.Undo, newest change first so a later edit to
// the same file is undone before an earlier one — the same order
// UndoChange itself expects hunks to disappear in. It stops at the first
// refusal, keeping what was already reverted; whenever anything was
// reverted, reportChecks fills in how (or whether) the result was
// verified, so a revert never finishes silent about it.
func (a *Agent) UndoSelection(ctx context.Context, sel Selection) (UndoReport, error) {
	if len(sel.Undo) == 0 {
		return UndoReport{}, ErrNothingSelected
	}
	undo := append([]Change(nil), sel.Undo...)
	sort.Slice(undo, func(i, j int) bool { return undo[i].Seq > undo[j].Seq })
	var rep UndoReport
	for i, c := range undo {
		done, err := a.UndoChange(c.Seq)
		if err != nil {
			var remaining []string
			for _, r := range undo[i:] {
				remaining = append(remaining, fmt.Sprintf("#%d", r.Seq))
			}
			rep.Refused = fmt.Sprintf("%v (not reverted: %s)", err, strings.Join(remaining, ", "))
			break
		}
		rep.Reverted = append(rep.Reverted, done)
	}
	if len(rep.Reverted) > 0 {
		a.reportChecks(ctx, &rep, sel)
	}
	return rep, nil
}

// reportChecks fills in rep's verification fields after at least one
// change from a selection was reverted. It runs the project's verify gate
// when one is trusted and configured; otherwise it explains why not
// (VerifyUntrusted, or nothing configured at all) and, since no checks
// ran, falls back to language-server diagnostics through a.Diagnose when
// one is wired up — the same hook a file tool's own edit uses (see
// agent.go) — or admits plainly that nothing verified the result when
// there is no such hook either.
//
// The fallback covers every file any of sel's candidates touches, reverted
// or kept, not only the reverted ones: a kept change can just as easily
// stop building once an undone neighbor's definition disappears under it
// (see detectCoupling), and checking only the reverted side would report
// "no problems" without having looked at the file that actually broke.
// Each file gets its own line naming it, so the report never reads as a
// bare "no problems" when some touched file was never examined at all.
func (a *Agent) reportChecks(ctx context.Context, rep *UndoReport, sel Selection) {
	if a.Verify.Enabled() {
		res := a.Verify.Run(ctx)
		ok := res.OK()
		rep.ChecksOK = &ok
		rep.Checks = res.Summary()
		return
	}
	if a.VerifyUntrusted {
		rep.CheckNote = fmt.Sprintf("the project's %s is not trusted, so checks did not run (/verify trust)", verify.ProjectFile)
	} else {
		rep.CheckNote = "no checks configured (/verify set <cmd>)"
	}
	paths := selectionTouchedPaths(sel)
	if a.Diagnose == nil || len(paths) == 0 {
		rep.Diagnostics = "nothing verified the result"
		return
	}
	cwd := processCwd()
	var notes []string
	for _, p := range paths {
		note := a.Diagnose(ctx, p)
		if note == "" {
			note = "not checked (no diagnostics available for this file)"
		}
		notes = append(notes, fmt.Sprintf("%s: %s", relTo(cwd, p), note))
	}
	rep.Diagnostics = strings.Join(notes, "\n")
}

// pendingUndo is the last selection shown by "/undo about", kept so
// "/undo apply" can act on exactly what the user saw rather than asking
// the model again (which could disagree with itself, or fail where the
// first call did not). fingerprint is the candidate seqs at preview time;
// apply refuses once they no longer match, rather than silently reverting
// a different set of changes than the one shown.
type pendingUndo struct {
	description string
	fingerprint string
	sel         Selection
}

// ErrNoPendingUndo is returned by ApplyPendingUndo when no "/undo about"
// has been previewed yet this session (or it was already applied).
var ErrNoPendingUndo = errors.New("agent: no previewed undo to apply; run /undo about <description> first")

// ErrUndoSelectionStale is returned by ApplyPendingUndo when the
// session's undoable changes moved since the preview: something was
// undone, reverted, or added.
var ErrUndoSelectionStale = errors.New("agent: the session's changes moved since /undo about; run it again")

// ErrUndoNeedsForce is returned by ApplyPendingUndo when the previewed
// selection has a change the model flagged as Mixed, or the deterministic
// coupling guard raised a Warning, and force was not passed: acting on
// either without a deliberate override risks leaving the tree half
// consistent, so the caller must re-run as "/undo apply --force".
var ErrUndoNeedsForce = errors.New("agent: this selection has mixed changes or coupling warnings; rerun as /undo apply --force to proceed anyway")

// PreviewUndoAbout selects changes matching description and remembers the
// selection as the session's pending undo, for a later ApplyPendingUndo.
func (a *Agent) PreviewUndoAbout(ctx context.Context, description string) (Selection, error) {
	sel, err := a.SelectChanges(ctx, description)
	if err != nil {
		return Selection{}, err
	}
	a.pendingUndo = &pendingUndo{description: description, fingerprint: fingerprintChanges(a.undoCandidates()), sel: sel}
	return sel, nil
}

// ApplyPendingUndo reverts the selection from the last PreviewUndoAbout.
// Unless force is true, it refuses when the selection has any Mixed
// change or coupling Warning, leaving the preview in place so the caller
// can re-run with force rather than having to preview again.
func (a *Agent) ApplyPendingUndo(ctx context.Context, force bool) (UndoReport, error) {
	p := a.pendingUndo
	if p == nil {
		return UndoReport{}, ErrNoPendingUndo
	}
	if fingerprintChanges(a.undoCandidates()) != p.fingerprint {
		return UndoReport{}, ErrUndoSelectionStale
	}
	if !force && (len(p.sel.Mixed) > 0 || len(p.sel.Warnings) > 0) {
		return UndoReport{}, ErrUndoNeedsForce
	}
	a.pendingUndo = nil
	return a.UndoSelection(ctx, p.sel)
}

// TellModelUndone adds a note to the conversation naming what a /undo
// apply reverted, so the model does not go on believing those changes are
// still there. Call it between turns.
func (a *Agent) TellModelUndone(rep UndoReport) {
	if len(rep.Reverted) == 0 {
		return
	}
	a.AddContext("I undid some of my earlier changes based on your description:\n" + strings.Join(rep.Reverted, "\n") + "\nLater changes to the same files stayed.")
}

// fingerprintChanges identifies the set of undoable changes at a moment
// in time, cheaply enough to call on every preview and apply: it changes
// whenever a candidate is undone, added, or removed.
func fingerprintChanges(candidates []Change) string {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = fmt.Sprintf("%d", c.Seq)
	}
	return strings.Join(ids, ",")
}

// processCwd is the working directory for relativising paths shown to the
// model in buildSelectPrompt; the empty string (meaning "show absolute
// paths") when it cannot be read.
func processCwd() string {
	d, _ := os.Getwd()
	return d
}

// relTo shortens path relative to cwd for display, leaving it absolute
// when it is not under cwd or cwd is unknown.
func relTo(cwd, path string) string {
	if cwd == "" {
		return path
	}
	if r, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// FormatSelection renders a selection for confirmation: every candidate
// considered, numbered, with its path, reason and line counts, marked
// undo, keep or mixed, in seq order so the listing reads like /changes
// does, followed by the mixed changes' reasons and any coupling warnings
// shown prominently, since either one blocks /undo apply short of
// --force.
func FormatSelection(cwd string, sel Selection) string {
	all := append(append([]Change(nil), sel.Undo...), sel.Keep...)
	for _, m := range sel.Mixed {
		all = append(all, m.Change)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	mark := make(map[int]string, len(all))
	for _, c := range sel.Undo {
		mark[c.Seq] = "undo"
	}
	for _, c := range sel.Keep {
		mark[c.Seq] = "keep"
	}
	for _, m := range sel.Mixed {
		mark[m.Change.Seq] = "mixed"
	}
	var sb strings.Builder
	if sel.Fallback {
		sb.WriteString("no model available for this: matched by keyword overlap, not meaning\n")
	}
	for _, c := range all {
		add, del := c.Stats()
		fmt.Fprintf(&sb, "[%-4s] #%-3d %-30s +%d -%d", mark[c.Seq], c.Seq, relTo(cwd, c.Path), add, del)
		if c.Intent != "" {
			fmt.Fprintf(&sb, "  · %s", c.Intent)
		}
		sb.WriteString("\n")
	}
	if len(all) == 0 {
		sb.WriteString("no changes to consider\n")
	}
	if sel.Why != "" {
		fmt.Fprintf(&sb, "\n%s\n", sel.Why)
	}
	if len(sel.Mixed) > 0 {
		sb.WriteString("\nmixed: these changes contain both matching and unrelated edits and were put on neither side:\n")
		for _, m := range sel.Mixed {
			fmt.Fprintf(&sb, "  #%d %s\n", m.Change.Seq, m.Why)
		}
	}
	if len(sel.Warnings) > 0 {
		sb.WriteString("\nwarning:\n")
		for _, w := range sel.Warnings {
			fmt.Fprintf(&sb, "  %s\n", w)
		}
	}
	if len(sel.Unavailable) > 0 {
		sb.WriteString("\nunavailable: these changes could not be located and were left out of undo and keep:\n")
		for _, u := range sel.Unavailable {
			fmt.Fprintf(&sb, "  #%d %s\n", u.Change.Seq, u.Why)
		}
	}
	switch {
	case len(sel.Undo) == 0 && len(sel.Mixed) == 0:
		sb.WriteString("\nnothing matched that description; /undo apply would do nothing\n")
	case len(sel.Mixed) > 0 || len(sel.Warnings) > 0:
		sb.WriteString("\n/undo apply refuses while the above stands; /undo apply --force overrides it\n")
	default:
		sb.WriteString("\n/undo apply reverts the changes marked undo\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatUndoReport renders what UndoSelection did: what was reverted,
// where it stopped, and — prominently, since staying silent about it is
// exactly the defect this guards against — how (or whether) the result
// was verified afterwards.
func FormatUndoReport(rep UndoReport) string {
	var sb strings.Builder
	for _, r := range rep.Reverted {
		fmt.Fprintf(&sb, "↶ %s\n", r)
	}
	if len(rep.Reverted) == 0 && rep.Refused == "" {
		sb.WriteString("nothing was reverted\n")
	}
	if rep.Refused != "" {
		fmt.Fprintf(&sb, "stopped: %s\n", rep.Refused)
	}
	switch {
	case rep.Checks != "":
		fmt.Fprintf(&sb, "%s\n", rep.Checks)
	case rep.CheckNote != "":
		fmt.Fprintf(&sb, "%s\n", rep.CheckNote)
		if rep.Diagnostics != "" {
			fmt.Fprintf(&sb, "%s\n", rep.Diagnostics)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// UndoArgs is "/undo"'s parsed arguments, shared by every front-end (see
// ParseUndoArgs) so "/undo ..." means exactly the same thing in the
// terminal and the web UI. The zero value is a bare "/undo": revert the
// last turn's whole checkpoint.
type UndoArgs struct {
	// Preview lists what a bare /undo would revert, without touching
	// anything.
	Preview bool
	// N is a specific /changes number to revert on its own; zero when
	// not given.
	N int
	// About previews the selection matching Description, without
	// touching anything (see PreviewUndoAbout). About is set whenever the
	// first word is "about", even with an empty Description — the caller
	// reports that as its own usage error, worded for its front-end.
	About       bool
	Description string
	// Apply reverts the selection from the last About (see
	// ApplyPendingUndo). Force is --force: proceed even though the
	// selection has a Mixed change or a coupling Warning.
	Apply bool
	Force bool
}

// errUndoArgsUsage documents /undo's argument shape (kept in step with
// slash.Command "/undo" and webArgs["/undo"]).
var errUndoArgsUsage = errors.New("usage: /undo [preview | n | about <description> | apply [--force]]")

// ParseUndoArgs parses the text after "/undo" (leading/trailing space
// trimmed by the caller or here either way): the one shared parser both
// the terminal and the web UI dispatch through, so a spelling accepted by
// one is accepted, and means the same thing, in the other.
func ParseUndoArgs(rest string) (UndoArgs, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return UndoArgs{}, nil
	}
	fields := strings.Fields(rest)
	switch fields[0] {
	case "preview":
		if len(fields) != 1 {
			return UndoArgs{}, errUndoArgsUsage
		}
		return UndoArgs{Preview: true}, nil
	case "about":
		return UndoArgs{About: true, Description: strings.TrimSpace(strings.TrimPrefix(rest, "about"))}, nil
	case "apply":
		a := UndoArgs{Apply: true}
		for _, f := range fields[1:] {
			if f != "--force" {
				return UndoArgs{}, errUndoArgsUsage
			}
			a.Force = true
		}
		return a, nil
	default:
		if n, err := strconv.Atoi(fields[0]); err == nil && len(fields) == 1 {
			return UndoArgs{N: n}, nil
		}
		return UndoArgs{}, errUndoArgsUsage
	}
}
