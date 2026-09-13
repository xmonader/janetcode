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
// Why is one line explaining the split, Fallback marks a selection made
// by keyword overlap rather than a model, because none was configured or
// its reply could not be used, and Warnings is the deterministic coupling
// guard's findings (see detectCoupling) — independent of how the split
// was made.
type Selection struct {
	Undo     []Change
	Keep     []Change
	Mixed    []Mixed
	Why      string
	Fallback bool
	Warnings []string
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
	// Diagnostics is the language-server fallback's report, consulted
	// only when CheckNote is set (no verify check ran): one note per
	// reverted file that has a running language server, or a plain
	// admission that nothing verified the result when no diagnose hook
	// is available at all. Empty whenever Checks ran.
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
	candidates := a.undoCandidates()
	if len(candidates) == 0 {
		return Selection{}, ErrNoChangesToUndo
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

// rename is one hunk's plausible rename: an identifier dropped and a
// different one added in its place.
type rename struct{ from, to string }

// renames lists the changes a Change's hunks look like renames of: for
// each hunk, the identifiers removed and not re-added, and those added
// and not previously present. When a hunk has exactly one of each, it is
// treated as unambiguous — from was renamed to to. A hunk with more than
// one candidate on either side is skipped rather than guessing which
// pairs with which.
func renames(c Change) []rename {
	var out []rename
	for _, h := range c.Hunks() {
		removed := tokensWithPrefix(h.Lines, '-')
		added := tokensWithPrefix(h.Lines, '+')
		var onlyRemoved, onlyAdded []string
		for t := range removed {
			if !added[t] {
				onlyRemoved = append(onlyRemoved, t)
			}
		}
		for t := range added {
			if !removed[t] {
				onlyAdded = append(onlyAdded, t)
			}
		}
		if len(onlyRemoved) == 1 && len(onlyAdded) == 1 {
			out = append(out, rename{from: onlyRemoved[0], to: onlyAdded[0]})
		}
	}
	return out
}

// introduces reports whether any of c's added lines mention tok — c
// either introduces it or uses one already introduced elsewhere.
func introduces(c Change, tok string) bool {
	for _, h := range c.Hunks() {
		if tokensWithPrefix(h.Lines, '+')[tok] {
			return true
		}
	}
	return false
}

// detectCoupling is undo selection's guard independent of the model's own
// judgement: for each identifier a change being undone looks like it
// renamed (see renames), if some other, different-file change being kept
// also carries that new name, undoing the first alone would leave the
// second referring to a name the revert just took away (or, read the
// other way, calling a name the surviving code never defines). Each such
// pair is named once, in a stable order, so the caller can refuse to
// proceed without a deliberate override. This only catches the direction
// where the rename is on the undone side; a rename kept while its use is
// undone is not flagged, to keep the guard simple, token-based and low on
// false positives rather than exhaustive.
func detectCoupling(sel Selection) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range sel.Undo {
		for _, rn := range renames(u) {
			for _, k := range sel.Keep {
				if k.Path == u.Path || !introduces(k, rn.to) {
					continue
				}
				key := fmt.Sprintf("%d|%d|%s", u.Seq, k.Seq, rn.to)
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, fmt.Sprintf("#%d and #%d both carry the rename %s→%s; undoing only #%d would leave #%d calling %s", u.Seq, k.Seq, rn.from, rn.to, u.Seq, k.Seq, rn.to))
			}
		}
	}
	sort.Strings(out)
	return out
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
	var revertedPaths []string
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
		revertedPaths = append(revertedPaths, c.Path)
	}
	if len(rep.Reverted) > 0 {
		a.reportChecks(ctx, &rep, revertedPaths)
	}
	return rep, nil
}

// reportChecks fills in rep's verification fields after at least one
// change from a selection was reverted. It runs the project's verify
// gate when one is trusted and configured; otherwise it explains why not
// (VerifyUntrusted, or nothing configured at all) and, since no checks
// ran, falls back to language-server diagnostics for the reverted files
// through a.Diagnose when one is wired up — the same hook a file tool's
// own edit uses (see agent.go) — or admits plainly that nothing verified
// the result when there is no such hook either.
func (a *Agent) reportChecks(ctx context.Context, rep *UndoReport, paths []string) {
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
	if a.Diagnose == nil {
		rep.Diagnostics = "nothing verified the result"
		return
	}
	seen := map[string]bool{}
	var notes []string
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if note := a.Diagnose(ctx, p); note != "" {
			notes = append(notes, note)
		}
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
