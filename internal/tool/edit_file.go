package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// EditFile replaces an exact substring in a file.
type EditFile struct {
	Root string
	WS   *Workspace
	// Seen, when set, refuses to edit a file that changed since the agent
	// last read or wrote it.
	Seen *Seen
	// NoLines withdraws the line-range form, leaving only exact-string
	// matching. It exists so the two can be measured against each other.
	NoLines bool
	Cache   *IOCache
	// ThenRun advertises then_run (see ThenRunKey).
	ThenRun bool
}

func (EditFile) Name() string { return "edit_file" }
func (t EditFile) Description() string {
	d := "Replace text inside one file, named by path. Each change needs a place to go as well as the text to write: old_string must match exactly once (including whitespace) unless replace_all is true; it fails if there is no match or the match is ambiguous. To make several changes to the same file, pass them all in edits — they are applied in order and either all land or none do, which is one call instead of one per change. The result shows the file around what changed, so there is usually no need to read it back."
	if !t.NoLines {
		d += " A change can be located by line instead of by text: when you already know the line numbers — read_file gives them to you — give from_line and to_line instead of old_string, and only the replacement text: retyping the old text to say where an edit goes is the single most expensive thing you write. Line numbers always refer to the file as it is before this call, so a list of line edits does not have to account for its own shifting."
	}
	return d
}
func (EditFile) Mutating() bool { return true }

// Schema describes one edit's fields once. An edit can be given at the
// top level or as an item of edits, and the two took the same fields with
// the same descriptions written out twice — a kilobyte of the request,
// every request, saying nothing the first copy had not said. The items
// list the fields with their types only and point at the descriptions
// above.
func (t EditFile) Schema() json.RawMessage {
	locator := `"old_string":{"type":"string","description":"Exact text to find"},`
	bare := `"old_string":{"type":"string"},`
	if !t.NoLines {
		locator = `"old_string":{"type":"string","description":"Exact text to find; omit when giving from_line and to_line"},
"from_line":{"type":"integer","description":"First line to replace, 1-based and inclusive, as numbered by read_file. Cheaper than old_string: give this and to_line instead of retyping the text"},
"to_line":{"type":"integer","description":"Last line to replace, 1-based and inclusive; equal to from_line replaces one line"},
"from_anchor":{"type":"string","description":"First line to replace, as the mark read_file showed beside it (12:abc). Survives earlier edits moving the line, and is refused rather than guessed if the line has changed"},
"to_anchor":{"type":"string","description":"Last line to replace, as its mark; equal to from_anchor replaces one line"},`
		bare = `"old_string":{"type":"string"},
"from_line":{"type":"integer"},
"to_line":{"type":"integer"},
"from_anchor":{"type":"string"},
"to_anchor":{"type":"string"},`
	}
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"File path"},
` + locator + `
"new_string":{"type":"string","description":"The text to write. Pair it with old_string (or from_line/to_line) saying where it goes; new_string on its own is rejected"},
"replace_all":{"type":"boolean","description":"Replace every occurrence (default false); only with old_string"},
` + t.thenRun() + `"edits":{"type":"array","description":"Several edits to this one file; use instead of a single edit when you have more than one change to make. Each item takes the same fields as above, and every edit must locate itself the same way — all by old_string, or all by line range","items":{
"type":"object","properties":{
` + bare + `
"new_string":{"type":"string"},
"replace_all":{"type":"boolean"}},
"required":["new_string"],"additionalProperties":false}}},
"required":["path"],"additionalProperties":false}`)
}

func (t EditFile) thenRun() string {
	if t.ThenRun {
		return thenRunProp
	}
	return ""
}

type editPlan struct {
	path, display string
	before, after string
	replacements  int
	// removed is what a line-range edit took out, quoted back in the
	// result. A range is a claim about where the edit goes that nothing
	// in the input verifies, so the answer says what it actually hit —
	// otherwise a miscounted line is a silent wrong edit.
	removed []string
	// notes records any rung of the matching ladder below an exact match
	// that a replacement needed, so the model is told its quoted text was
	// not what the file holds.
	notes []string
	// shape is the file's byte-order mark, line endings and charset, put
	// back when the edit is written.
	shape form
}

// oneEdit is a single replacement, located either by the text it replaces
// or by the lines it covers.
type oneEdit struct {
	Old        string `json:"old_string"`
	New        string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	From       int    `json:"from_line"`
	To         int    `json:"to_line"`
	FromAnchor string `json:"from_anchor"`
	ToAnchor   string `json:"to_anchor"`
}

// byLine reports whether the edit locates itself by position rather than
// by the text it replaces. An anchor is a position too — a sturdier one.
func (e oneEdit) byLine() bool {
	return e.From > 0 || e.To > 0 || e.FromAnchor != "" || e.ToAnchor != ""
}

// settleLocators decides, for an edit that gave both old_string and a
// position, which of the two it goes by. Models do send both — the line
// numbers they read and the text they are replacing — and refusing that
// cost a whole round trip to be told to drop one, for an edit whose intent
// was never in doubt. The text wins when it occurs exactly once in the
// file (or replace_all asks for every occurrence): it is the stricter of
// the two. Otherwise the position stands, and the text is dropped.
func settleLocators(before string, edits []oneEdit, noLines bool) []oneEdit {
	for i, e := range edits {
		if e.Old == "" || !e.byLine() {
			continue
		}
		if noLines || e.ReplaceAll || strings.Count(before, e.Old) == 1 {
			e.From, e.To, e.FromAnchor, e.ToAnchor = 0, 0, "", ""
		} else {
			e.Old = ""
		}
		edits[i] = e
	}
	return edits
}

// resolve turns an edit's anchors into line numbers against the file as it
// is now, leaving a plain line range alone.
func (e oneEdit) resolve(lines []string) (oneEdit, error) {
	if e.FromAnchor == "" && e.ToAnchor == "" {
		return e, nil
	}
	first, last := e.FromAnchor, e.ToAnchor
	if first == "" {
		first = last
	}
	if last == "" {
		last = first
	}
	ref, err := parseAnchor(first)
	if err != nil {
		return e, err
	}
	from, err := resolveAnchor(ref, lines)
	if err != nil {
		return e, err
	}
	ref2, err := parseAnchor(last)
	if err != nil {
		return e, err
	}
	to, err := resolveAnchor(ref2, lines)
	if err != nil {
		return e, err
	}
	e.From, e.To = from, to
	return e, nil
}

// apply performs one replacement on content, reporting how many times the
// text was found. The errors name the edit's position when there are
// several, so the model can tell which of them did not fit.
func (e oneEdit) apply(content, display string, index, total int) (string, int, string, error) {
	where := ""
	if total > 1 {
		where = fmt.Sprintf("edit %d of %d: ", index+1, total)
	}
	if e.Old == "" {
		// A replacement with no location at all is the common shape of
		// this mistake; saying so beats naming the field that is empty.
		return "", 0, "", fmt.Errorf("%sthis edit says what to write but not where: give old_string (the exact text to replace) or from_line and to_line", where)
	}
	if e.Old == e.New {
		return "", 0, "", fmt.Errorf("%sold_string and new_string are identical", where)
	}
	if n := strings.Count(content, e.Old); n > 0 {
		// Overlapping occurrences count for uniqueness: strings.Count does
		// not see them, so "aba" was unique in "ababa" and the first was
		// edited when the model may have meant the second.
		if k := countOverlapping(content, e.Old); k > 1 && !e.ReplaceAll {
			return "", 0, "", fmt.Errorf("%sold_string matches %d times in %s; make it unique or set replace_all", where, k, display)
		}
		switch {
		case e.ReplaceAll:
			return strings.ReplaceAll(content, e.Old, e.New), n, "", nil
		}
		return strings.Replace(content, e.Old, e.New, 1), n, "", nil
	}
	// Exact matching missed. Each rung below is more forgiving than the
	// last and the first one that finds the text wins, so the outcome does
	// not depend on how far down the ladder the search happened to get.
	for _, rung := range []struct {
		find func(string, string) [][2]int
		note string
	}{
		{matchFolded, "matched after folding equivalent characters (dashes, quotes, spaces): old_string did not match the file byte for byte"},
		{matchLines, "matched ignoring trailing whitespace on each line: old_string did not match the file byte for byte"},
	} {
		spans := dropOverlaps(rung.find(content, e.Old))
		if len(spans) == 0 {
			continue
		}
		if len(spans) > 1 && !e.ReplaceAll {
			return "", 0, "", fmt.Errorf("%sold_string matches %d times in %s (%s); make it unique or set replace_all", where, len(spans), display, rung.note)
		}
		if !e.ReplaceAll {
			spans = spans[:1]
		}
		return replaceSpans(content, spans, e.New), len(spans), where + rung.note, nil
	}
	msg := fmt.Sprintf("%sold_string not found in %s", where, display)
	if nm := nearMiss(content, e.Old, display); nm != "" {
		msg += ".\n" + nm
	}
	return "", 0, "", errors.New(msg)
}

// countOverlapping counts the occurrences of sub in s, overlapping ones
// included. sub must not be empty.
func countOverlapping(s, sub string) int {
	n := 0
	for {
		i := strings.Index(s, sub)
		if i < 0 {
			return n
		}
		n++
		s = s[i+1:]
	}
}

// checkSeen resolves the edit's path and asks Seen whether the file is
// still the one the agent looked at.
func (t EditFile) checkSeen(ctx context.Context, input json.RawMessage) error {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return nil // the plan will report what is wrong with the input
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return nil
	}
	return t.Seen.CheckAt(ctx, t.WS, p, displayPath(rootOf(t.Root, t.WS), in.Path), "edit")
}

func (t EditFile) plan(ctx context.Context, input json.RawMessage) (*editPlan, error) {
	input = repairEdits(input)
	var in struct {
		Path       string    `json:"path"`
		Old        string    `json:"old_string"`
		New        string    `json:"new_string"`
		ReplaceAll bool      `json:"replace_all"`
		From       int       `json:"from_line"`
		To         int       `json:"to_line"`
		FromAnchor string    `json:"from_anchor"`
		ToAnchor   string    `json:"to_anchor"`
		Edits      []oneEdit `json:"edits"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	edits := in.Edits
	if in.Old != "" || in.New != "" || in.From > 0 || in.To > 0 || in.FromAnchor != "" || in.ToAnchor != "" {
		// The single-edit form and the list are the same thing; a call
		// that uses both is applying the loose one first.
		edits = append([]oneEdit{{Old: in.Old, New: in.New, ReplaceAll: in.ReplaceAll, From: in.From, To: in.To, FromAnchor: in.FromAnchor, ToAnchor: in.ToAnchor}}, edits...)
	}
	for i, e := range edits {
		if err := checkWriteSize(fmt.Sprintf("new_string of edit %d", i+1), len(e.New), "make the replacement smaller or split it across edits"); err != nil {
			return nil, err
		}
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("no edits: give old_string and new_string, or from_line and to_line, or a list in edits")
	}
	p, err := resolveOn(t.WS, t.Root, in.Path)
	if err != nil {
		return nil, err
	}
	data, err := readFileOf(ctx, t.WS, p)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, &ErrPriorRead{Path: displayPath(rootOf(t.Root, t.WS), in.Path), Verb: "edit", Cause: CauseBinary}
	}
	before, shape := decodeText(data)
	edits = settleLocators(before, edits, t.NoLines)
	lines := 0
	for _, e := range edits {
		if e.byLine() {
			lines++
		}
	}
	if lines > 0 && t.NoLines {
		return nil, fmt.Errorf("this session locates edits by old_string only")
	}
	if lines > 0 && lines != len(edits) {
		// Mixing the two would make "line 12" ambiguous — before or after
		// the string edits shifted it? Refusing is clearer than choosing.
		return nil, fmt.Errorf("every edit in one call must locate itself the same way: %d of %d give a line range", lines, len(edits))
	}
	if lines > 0 {
		// Anchors are resolved against the file as it is now, before any
		// of this batch is applied, so a list of them is written against
		// one consistent view exactly as a list of line numbers is.
		fileLines, _ := splitKeepingEnd(before)
		for i, e := range edits {
			r, err := e.resolve(fileLines)
			if err != nil {
				where := ""
				if len(edits) > 1 {
					where = fmt.Sprintf("edit %d of %d: ", i+1, len(edits))
				}
				return nil, fmt.Errorf("%s%w", where, err)
			}
			edits[i] = r
		}
		after, n, removed, err := applyLineEdits(before, edits)
		if err != nil {
			return nil, err
		}
		if err := checkWriteSize(fmt.Sprintf("the edited %s", displayPath(rootOf(t.Root, t.WS), in.Path)), len(after), "a file this large has to be edited with a bash command instead"); err != nil {
			return nil, err
		}
		return &editPlan{path: p, display: displayPath(rootOf(t.Root, t.WS), in.Path), before: before, after: after, replacements: n, removed: removed, shape: shape}, nil
	}
	content, total := before, 0
	var notes []string
	for i, e := range edits {
		updated, n, note, err := e.apply(content, in.Path, i, len(edits))
		if err != nil {
			return nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		content, total = updated, total+n
	}
	if err := checkWriteSize(fmt.Sprintf("the edited %s", displayPath(rootOf(t.Root, t.WS), in.Path)), len(content), "a file this large has to be edited with a bash command instead"); err != nil {
		return nil, err
	}
	return &editPlan{path: p, display: displayPath(rootOf(t.Root, t.WS), in.Path), before: before, after: content, replacements: total, notes: notes, shape: shape}, nil
}

// repairEdits fixes the two ways models malform the edits argument, both
// seen in real runs: sending the array as a JSON string because it encoded
// it twice, and sending a single edit object where a list is expected.
// Neither is ambiguous, and refusing them costs a whole round trip to say
// so, which is more expensive than the repair.
func repairEdits(input json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &obj) != nil {
		return input
	}
	raw, ok := obj["edits"]
	if !ok {
		return input
	}
	switch firstByte(raw) {
	case '"':
		var inner string
		if json.Unmarshal(raw, &inner) != nil {
			return input
		}
		trimmed := json.RawMessage(strings.TrimSpace(inner))
		if !json.Valid(trimmed) {
			return input
		}
		if firstByte(trimmed) == '{' {
			trimmed = json.RawMessage("[" + string(trimmed) + "]")
		}
		obj["edits"] = trimmed
	case '{':
		obj["edits"] = json.RawMessage("[" + string(raw) + "]")
	default:
		return input
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return input
	}
	return out
}

// firstByte is the first non-space byte, or 0.
func firstByte(raw json.RawMessage) byte {
	for _, b := range raw {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return b
		}
	}
	return 0
}

// applyLineEdits replaces line ranges. Every range is read against the file
// as it was before the call and they are applied from the bottom up, so a
// list of edits does not have to account for its own shifting — which is
// the whole reason a model can write one without re-reading the file.
// Overlapping ranges are refused rather than resolved: two edits claiming
// the same line is a mistake, not an intention.
func applyLineEdits(before string, edits []oneEdit) (string, int, []string, error) {
	lines, trailing := splitKeepingEnd(before)
	type span struct {
		from, to int
		text     string
		index    int
	}
	spans := make([]span, 0, len(edits))
	for i, e := range edits {
		where := ""
		if len(edits) > 1 {
			where = fmt.Sprintf("edit %d of %d: ", i+1, len(edits))
		}
		from, to := e.From, e.To
		if from == 0 {
			from = to
		}
		if to == 0 {
			to = from
		}
		switch {
		case e.Old != "" || e.ReplaceAll:
			return "", 0, nil, fmt.Errorf("%sgive either a position (line or anchor) or old_string, not both", where)
		case from < 1:
			return "", 0, nil, fmt.Errorf("%sfrom_line is %d; lines are numbered from 1", where, from)
		case to < from:
			return "", 0, nil, fmt.Errorf("%sto_line %d is before from_line %d", where, to, from)
		case from > len(lines):
			return "", 0, nil, fmt.Errorf("%sfrom_line %d is past the end of the file, which has %d line(s)", where, from, len(lines))
		case to > len(lines):
			return "", 0, nil, fmt.Errorf("%sto_line %d is past the end of the file, which has %d line(s)", where, to, len(lines))
		}
		spans = append(spans, span{from: from, to: to, text: e.New, index: i})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].from != spans[j].from {
			return spans[i].from < spans[j].from
		}
		return spans[i].index < spans[j].index
	})
	// A range repeated with the same replacement is a duplicate, not a
	// conflict: the model said the same thing twice and meant it once.
	// Refusing those costs a round trip to be told something unambiguous,
	// and models do repeat themselves. Overlapping ranges that disagree
	// are still refused, because there is no way to choose between them.
	kept := spans[:0]
	for i, sp := range spans {
		if i > 0 {
			prev := kept[len(kept)-1]
			if sp.from == prev.from && sp.to == prev.to && sp.text == prev.text {
				continue
			}
			if sp.from <= prev.to {
				return "", 0, nil, fmt.Errorf("edits %d and %d both cover line %d with different replacements; merge them or target separate lines", prev.index+1, sp.index+1, sp.from)
			}
		}
		kept = append(kept, sp)
	}
	spans = kept
	out := append([]string(nil), lines...)
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		var repl []string
		if sp.text != "" {
			repl = strings.Split(strings.TrimSuffix(sp.text, "\n"), "\n")
		}
		out = append(out[:sp.from-1], append(repl, out[sp.to:]...)...)
	}
	joined := strings.Join(out, "\n")
	if trailing && joined != "" {
		joined += "\n"
	}
	var removed []string
	for _, sp := range spans {
		label := "lines"
		if sp.from == sp.to {
			label = "line"
		}
		removed = append(removed, fmt.Sprintf("%s %s: %s", label, lineRange(sp.from, sp.to), quoteLines(lines[sp.from-1:sp.to])))
	}
	return joined, len(spans), removed, nil
}

// lineRange renders one line or a span.
func lineRange(from, to int) string {
	if from == to {
		return fmt.Sprint(from)
	}
	return fmt.Sprintf("%d-%d", from, to)
}

// quoteLines shows what a range held, short enough to read at a glance.
func quoteLines(ls []string) string {
	const max = 3
	suffix := ""
	if len(ls) > max {
		ls, suffix = ls[:max], fmt.Sprintf(" … and %d more line(s)", len(ls)-max)
	}
	// A copy: ls aliases the caller's lines, and clipping in place would
	// rewrite the file's own text. The clip is by rune, so a line cut
	// short does not end in half a character.
	shown := make([]string, len(ls))
	for i, l := range ls {
		shown[i] = textfmt.ClipBytes(l, 90)
	}
	return strings.TrimSpace(strings.Join(shown, " ⏎ ")) + suffix
}

// splitKeepingEnd splits into lines and reports whether the file ended
// with a newline, so rewriting it does not silently add or drop one.
func splitKeepingEnd(s string) ([]string, bool) {
	if s == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(s, "\n")
	if trailing {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), trailing
}

// window renders the file around everything the edit changed, so the model
// can see the result in place instead of reading the file back. A change
// that is spread over more of the file than is worth quoting is reported by
// its size alone.
func window(after string, first, last int) string {
	const pad, cap = 4, 80
	lines := strings.Split(after, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	from, to := max(0, first-pad), min(len(lines), last+pad)
	if from >= to {
		return ""
	}
	if to-from > cap {
		return fmt.Sprintf("(the change spans lines %d-%d, too much to quote)", from+1, to)
	}
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	return strings.TrimRight(b.String(), "\n")
}

// changed reports the first and last line of after that differ from before,
// as 0-based indices into after's lines. It returns ok=false when the two
// are identical.
func changed(before, after string) (first, last int, ok bool) {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
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
	return i, max(i, k), true
}

func (t EditFile) Run(ctx context.Context, input json.RawMessage) (string, error) {
	// The state of the file is checked before the plan is built: a file
	// that vanished since it was read must say so, not fail as an open
	// error with no explanation of what happened to it.
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
	out := fmt.Sprintf("Edited %s (%s, +%d -%d lines)", pl.display, textfmt.Plural(pl.replacements, "replacement"), added, removed)
	for _, r := range pl.removed {
		out += "\nreplaced " + r
	}
	for _, n := range pl.notes {
		out += "\n[" + n + "]"
	}
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

// Guard implements Guard: an edit to one of janetcode's own control
// surfaces asks, whatever the permission mode.
func (t EditFile) Guard(input json.RawMessage) (bool, string) {
	return guardSelfModify(rootOf(t.Root, t.WS), input)
}

// Preview implements Previewer with a unified diff of the edit.
// Touches reports the file the edit will rewrite.
func (t EditFile) Touches(input json.RawMessage) ([]string, error) {
	return touchedPathOn(t.WS, t.Root, input)
}

func (t EditFile) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	return diff.Unified(pl.display, pl.before, pl.after, 3), nil
}

// Result computes what applying the edit would produce, without writing:
// the resolved path, and the file's content before and after. It shares
// plan's exact-match and line-range logic, so this is never out of step
// with what Run would actually do. internal/impact uses it to analyze the
// reach of an edit before it is approved. The read it does to work out
// "before" can cross a network (a remote workspace), so it takes the
// caller's context rather than inventing one.
func (t EditFile) Result(ctx context.Context, input json.RawMessage) (path, before, after string, err error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", "", "", err
	}
	return pl.path, pl.before, pl.after, nil
}
