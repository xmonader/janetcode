package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/xmonader/janetcode/internal/diff"
)

// ApplyPatch applies a multi-file patch in the codex-style envelope
// (*** Begin Patch ... *** End Patch). Some models are trained on that
// format and emit it more reliably than any JSON edit schema, so the tool
// is offered only to them (see PatchModel); everyone else never pays its
// schema tokens.
//
// It is as safe as edit_file and write_file: every path is confined to the
// workspace and checked against admission, an update or delete of a file
// the agent read is refused when the file changed since, and the whole
// patch is parsed, matched and staged in memory before the first byte is
// written. A write that fails midway restores the files already written.
// Each hunk's old block is found with the same match ladder edit_file uses
// (exact, folded characters, trailing whitespace; leading whitespace is
// never relaxed) and has to match exactly once, searching forward from the
// previous hunk of the same file.
type ApplyPatch struct {
	Root  string
	WS    *Workspace
	Seen  *Seen
	Cache *IOCache
}

func (ApplyPatch) Name() string { return "apply_patch" }
func (ApplyPatch) Description() string {
	return "Edit files with a patch: *** Begin Patch, then per file `*** Add File: p` (+lines), `*** Delete File: p`, or `*** Update File: p` (optional `*** Move to: q`) with `@@ context` hunks of ' ', '-', '+' lines, then *** End Patch. Hunk context must match the file and be unique."
}
func (ApplyPatch) Mutating() bool { return true }
func (ApplyPatch) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"The whole patch, from *** Begin Patch to *** End Patch"}},"required":["input"],"additionalProperties":false}`)
}

// PatchModel reports whether a model id belongs to a family trained on the
// patch envelope: gpt-*, o3*, o4* and codex*, whatever provider prefix
// ("openai/gpt-5") the id carries. extra are further path.Match globs from
// the configuration (patch_models) for models this list does not know.
func PatchModel(id string, extra ...string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	for _, p := range append([]string{"gpt-*", "o3", "o3-*", "o4", "o4-*", "codex*"}, extra...) {
		if ok, _ := path.Match(strings.ToLower(p), id); ok {
			return true
		}
	}
	return false
}

type patchInput struct {
	Input string `json:"input"`
}

type patchHunk struct {
	context string // text after @@, locates the hunk when non-empty
	lines   []patchLine
	eof     bool
}

type patchLine struct {
	op   byte // ' ', '-', '+'
	text string
}

type patchOp struct {
	kind  string // add, delete, update
	path  string
	move  string
	lines []string // add: file content
	hunks []patchHunk
}

const patchTruncated = "the patch looks cut off by the output-token limit (no *** End Patch); send a smaller patch, one or two files at a time. Nothing was applied."

// elisionPhrases are what a model writes in place of code it did not want
// to repeat. Matched case-insensitively against an added line.
var elisionPhrases = []string{
	"... existing", "...existing", "rest unchanged", "rest of the code", "rest of code",
	"rest of the file", "rest of file", "remains unchanged", "remaining code", "unchanged ...",
	"... rest", "...rest", "... remaining", "... unchanged", "same as before", "previous code",
}

// elisionBare is a line that is only dots, with an optional comment opener.
var elisionBare = regexp.MustCompile(`^(//|#|/\*|<!--|--)?\s*(\.\.\.|…)\s*(\*/|-->)?$`)

func isElision(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	for _, p := range elisionPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return elisionBare.MatchString(t)
}

func parsePatch(text string) ([]patchOp, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	begin := -1
	for i, l := range raw {
		if strings.TrimSpace(l) == "*** Begin Patch" {
			begin = i
			break
		}
	}
	if begin < 0 {
		return nil, fmt.Errorf("no *** Begin Patch line: the patch must start with *** Begin Patch and end with *** End Patch")
	}
	end := -1
	for i := len(raw) - 1; i > begin; i-- {
		if strings.TrimSpace(raw[i]) == "*** End Patch" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("%s", patchTruncated)
	}
	body := raw[begin+1 : end]
	var ops []patchOp
	var hunk *patchHunk
	cur := -1 // index in ops of the section taking lines, or -1
	flush := func() {
		if cur >= 0 && hunk != nil {
			ops[cur].hunks = append(ops[cur].hunks, *hunk)
		}
		hunk = nil
	}
	header := func(l, prefix string) (string, bool) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), prefix); ok {
			return strings.TrimSpace(rest), true
		}
		return "", false
	}
	for n, l := range body {
		if p, ok := header(l, "*** Add File:"); ok {
			flush()
			ops = append(ops, patchOp{kind: "add", path: p})
			cur = len(ops) - 1
			continue
		}
		if p, ok := header(l, "*** Delete File:"); ok {
			flush()
			ops = append(ops, patchOp{kind: "delete", path: p})
			cur = -1
			continue
		}
		if p, ok := header(l, "*** Update File:"); ok {
			flush()
			ops = append(ops, patchOp{kind: "update", path: p})
			cur = len(ops) - 1
			continue
		}
		if cur < 0 {
			if strings.TrimSpace(l) == "" {
				continue
			}
			return nil, fmt.Errorf("patch line %d: %q is outside any *** Add/Update/Delete File section", begin+n+2, l)
		}
		op := &ops[cur]
		if p, ok := header(l, "*** Move to:"); ok && op.kind == "update" && len(op.hunks) == 0 && hunk == nil {
			op.move = p
			continue
		}
		if strings.TrimSpace(l) == "*** End of File" {
			if hunk != nil {
				hunk.eof = true
			}
			continue
		}
		if op.kind == "add" {
			t := strings.TrimPrefix(l, "+") // unprefixed lines are tolerated
			if isElision(t) {
				return nil, elisionErr(op.path, t)
			}
			op.lines = append(op.lines, t)
			continue
		}
		if rest, ok := strings.CutPrefix(l, "@@"); ok {
			flush()
			hunk = &patchHunk{context: strings.TrimSpace(rest)}
			continue
		}
		if hunk == nil {
			hunk = &patchHunk{}
		}
		switch {
		case l == "":
			hunk.lines = append(hunk.lines, patchLine{' ', ""})
		case l[0] == '+':
			if isElision(l[1:]) {
				return nil, elisionErr(op.path, l[1:])
			}
			hunk.lines = append(hunk.lines, patchLine{'+', l[1:]})
		case l[0] == '-':
			hunk.lines = append(hunk.lines, patchLine{'-', l[1:]})
		case l[0] == ' ':
			hunk.lines = append(hunk.lines, patchLine{' ', l[1:]})
		default: // a context line that lost its leading space
			hunk.lines = append(hunk.lines, patchLine{' ', l})
		}
	}
	flush()
	if len(ops) == 0 {
		return nil, fmt.Errorf("the patch has no *** Add File, *** Update File or *** Delete File section")
	}
	for _, o := range ops {
		if o.path == "" {
			return nil, fmt.Errorf("a %s section has no file path", o.kind)
		}
		if o.kind == "update" && len(o.hunks) == 0 && o.move == "" {
			return nil, fmt.Errorf("*** Update File: %s has no hunks", o.path)
		}
	}
	return ops, nil
}

func elisionErr(file, line string) error {
	return fmt.Errorf("refused: an added line in %s is a placeholder (%q), not code. Write out the complete text; elision markers would be written into the file literally. Nothing was applied", file, strings.TrimSpace(line))
}

// patchFile is one file's staged state.
type patchFile struct {
	path, display string
	before, after string
	form          form
	existed       bool
	delete        bool
	origData      []byte // bytes on disk before, to roll back
}

type patchPlan struct {
	files []*patchFile // in first-touch order
	notes []string
	seen  []string // paths that must pass the read-before-edit check
}

func (t ApplyPatch) plan(ctx context.Context, input json.RawMessage) (*patchPlan, error) {
	var in patchInput
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	ops, err := parsePatch(in.Input)
	if err != nil {
		return nil, err
	}
	root := rootOf(t.Root, t.WS)
	pl := &patchPlan{}
	byPath := map[string]*patchFile{}
	load := func(rel string) (*patchFile, error) {
		p, err := resolveWriteOn(t.WS, t.Root, rel)
		if err != nil {
			return nil, err
		}
		if f, ok := byPath[p]; ok {
			return f, nil
		}
		f := &patchFile{path: p, display: displayPath(root, p)}
		data, err := readFileOf(ctx, t.WS, p)
		switch {
		case err == nil:
			if isBinary(data) {
				return nil, &ErrPriorRead{Path: f.display, Verb: "edit", Cause: CauseBinary}
			}
			f.existed, f.origData = true, data
			f.before, f.form = decodeText(data)
			f.after = f.before
		case os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR):
			f.form = form{Charset: "utf-8"}
		default:
			return nil, err
		}
		byPath[p] = f
		pl.files = append(pl.files, f)
		return f, nil
	}
	// created marks files that exist only in the staged state.
	created := map[*patchFile]bool{}
	for _, op := range ops {
		f, err := load(op.path)
		if err != nil {
			return nil, err
		}
		switch op.kind {
		case "add":
			if (f.existed || created[f]) && !f.delete {
				return nil, fmt.Errorf("*** Add File: %s already exists; use *** Update File to change it", f.display)
			}
			f.delete = false
			created[f] = true
			f.after = strings.Join(op.lines, "\n") + "\n"
			if len(op.lines) == 0 {
				f.after = ""
			}
		case "delete":
			if !f.existed && !created[f] {
				pl.notes = append(pl.notes, fmt.Sprintf("%s does not exist; nothing to delete", f.display))
				continue
			}
			if f.existed {
				pl.seen = append(pl.seen, f.path)
			}
			f.delete = true
		case "update":
			if !f.existed && !created[f] {
				return nil, fmt.Errorf("*** Update File: %s does not exist; use *** Add File to create it", f.display)
			}
			if f.delete {
				return nil, fmt.Errorf("*** Update File: %s was deleted earlier in the same patch", f.display)
			}
			if f.existed {
				pl.seen = append(pl.seen, f.path)
			}
			after, notes, err := applyHunks(f.after, op.hunks, f.display)
			if err != nil {
				return nil, err
			}
			f.after = after
			pl.notes = append(pl.notes, notes...)
			if op.move != "" {
				dst, err := load(op.move)
				if err != nil {
					return nil, err
				}
				if (dst.existed || created[dst]) && !dst.delete {
					return nil, fmt.Errorf("*** Move to: %s already exists", dst.display)
				}
				dst.after, dst.form, dst.delete = f.after, f.form, false
				created[dst] = true
				f.delete = true
			}
		}
	}
	for _, f := range pl.files {
		if !f.delete {
			if _, err := encodeChecked(f.after, f.form, "the patched "+f.display, "split the patch into smaller ones"); err != nil {
				return nil, err
			}
		}
	}
	return pl, nil
}

// applyHunks applies the hunks to LF text in order, each searching forward
// from where the previous one ended.
func applyHunks(content string, hunks []patchHunk, display string) (string, []string, error) {
	noNL := content != "" && !strings.HasSuffix(content, "\n")
	if noNL {
		content += "\n"
	}
	var notes []string
	cursor := 0
	for i, h := range hunks {
		n := i + 1
		if h.context != "" {
			at := findLineFrom(content, cursor, h.context)
			if at < 0 {
				return "", nil, hunkMiss(n, display, content, []string{h.context}, "its @@ context line was not found")
			}
			cursor = at
		}
		var oldLines []string
		oldText := ""
		for _, l := range h.lines {
			if l.op != '+' {
				oldLines = append(oldLines, l.text)
				oldText += l.text + "\n"
			}
		}
		if len(oldLines) == 0 {
			// Pure insertion: after the @@ context line when there is one,
			// else at the end of the file.
			at := len(content)
			if h.context != "" {
				at = cursor
			}
			var b strings.Builder
			for _, l := range h.lines {
				b.WriteString(l.text + "\n")
			}
			content = content[:at] + b.String() + content[at:]
			cursor = at + b.Len()
			continue
		}
		spans, rung := locate(content[cursor:], oldText, h.eof)
		if len(spans) == 0 {
			return "", nil, hunkMiss(n, display, content, oldLines, "")
		}
		if len(spans) > 1 {
			return "", nil, fmt.Errorf("hunk %d in %s is ambiguous: its old block matches %d places. Add more unique context lines (or an @@ line) around it", n, display, len(spans))
		}
		sp := [2]int{spans[0][0] + cursor, spans[0][1] + cursor}
		if rung != "" {
			notes = append(notes, fmt.Sprintf("hunk %d in %s matched %s", n, display, rung))
		}
		// Context lines keep the file's own text; only - and + change it.
		orig := strings.Split(strings.TrimSuffix(content[sp[0]:sp[1]], "\n"), "\n")
		var b strings.Builder
		k := 0
		for _, l := range h.lines {
			switch l.op {
			case ' ':
				b.WriteString(orig[k] + "\n")
				k++
			case '-':
				k++
			case '+':
				b.WriteString(l.text + "\n")
			}
		}
		content = content[:sp[0]] + b.String() + content[sp[1]:]
		cursor = sp[0] + b.Len()
	}
	if noNL {
		content = strings.TrimSuffix(content, "\n")
	}
	return content, notes, nil
}

// findLineFrom is the byte offset just after the first line at or beyond
// from that equals want once folded and trimmed, or -1.
func findLineFrom(content string, from int, want string) int {
	cs := splitLineSpans(content)
	k := key(strings.TrimSpace(want))
	for i, l := range cs.lines {
		if cs.starts[i] < from {
			continue
		}
		if key(strings.TrimSpace(l)) == k {
			return cs.starts[i+1]
		}
	}
	return -1
}

func aligned(content string, sp [2]int) bool {
	return (sp[0] == 0 || content[sp[0]-1] == '\n') && (sp[1] == len(content) || content[sp[1]-1] == '\n')
}

// locate runs edit_file's match ladder over whole-line blocks. A rung that
// finds anything decides; rung names the one used when it was not exact.
func locate(content, old string, eof bool) ([][2]int, string) {
	pick := func(sp [][2]int) [][2]int {
		var out [][2]int
		for _, s := range sp {
			if aligned(content, s) {
				out = append(out, s)
			}
		}
		out = dropOverlaps(out)
		if eof && len(out) > 1 {
			out = out[len(out)-1:]
		}
		return out
	}
	var exact [][2]int
	for i := 0; ; {
		j := strings.Index(content[i:], old)
		if j < 0 {
			break
		}
		exact = append(exact, [2]int{i + j, i + j + len(old)})
		i += j + 1
	}
	if s := pick(exact); len(s) > 0 {
		return s, ""
	}
	if s := pick(matchFolded(content, old)); len(s) > 0 {
		return s, "after folding look-alike characters"
	}
	if s := pick(matchLines(content, old)); len(s) > 0 {
		return s, "ignoring trailing whitespace"
	}
	return nil, ""
}

var wordRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

// closestLine is the file line sharing the most identifier-like tokens
// with want (token-overlap similarity), with its 1-based number; 0 when
// nothing overlaps enough to mean anything.
func closestLine(content, want string) (int, string) {
	wt := map[string]bool{}
	for _, w := range wordRe.FindAllString(want, -1) {
		wt[w] = true
	}
	if len(wt) == 0 {
		return 0, ""
	}
	best, bestAt, bestLine := 0.0, 0, ""
	for i, l := range strings.Split(content, "\n") {
		lt := map[string]bool{}
		for _, w := range wordRe.FindAllString(l, -1) {
			lt[w] = true
		}
		common := 0
		for w := range wt {
			if lt[w] {
				common++
			}
		}
		if s := float64(common) / float64(len(wt)+len(lt)-common); s > best {
			best, bestAt, bestLine = s, i+1, l
		}
	}
	if best < 0.3 {
		return 0, ""
	}
	return bestAt, bestLine
}

func hunkMiss(n int, display, content string, old []string, why string) error {
	msg := fmt.Sprintf("hunk %d in %s did not match", n, display)
	if why != "" {
		msg += ": " + why
	}
	if nm := nearMiss(content, strings.Join(old, "\n")+"\n", display); nm != "" {
		msg += ".\n" + nm
	} else {
		first := ""
		for _, l := range old {
			if strings.TrimSpace(l) != "" {
				first = l
				break
			}
		}
		if at, line := closestLine(content, first); at > 0 {
			msg += fmt.Sprintf(". The closest line in the file is %d: %q", at, strings.TrimSpace(line))
		}
	}
	return fmt.Errorf("%s\nRe-read the file and quote its lines exactly; nothing was applied", msg)
}

func (t ApplyPatch) checkSeen(ctx context.Context, pl *patchPlan) error {
	for _, p := range pl.seen {
		if err := t.Seen.CheckAt(ctx, t.WS, p, displayPath(rootOf(t.Root, t.WS), p), "edit"); err != nil {
			return err
		}
	}
	return nil
}

func (t ApplyPatch) Run(ctx context.Context, input json.RawMessage) (string, error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	if err := t.checkSeen(ctx, pl); err != nil {
		return "", err
	}
	host := func() Host {
		if t.WS != nil {
			return t.WS.Host()
		}
		return Local{}
	}()
	var written []*patchFile
	rollback := func() {
		for i := len(written) - 1; i >= 0; i-- {
			w := written[i]
			switch {
			case w.delete:
				_ = writeFileOf(ctx, t.WS, w.path, w.origData)
			case w.existed:
				_ = writeFileOf(ctx, t.WS, w.path, w.origData)
			default:
				_ = host.Remove(ctx, w.path)
			}
		}
	}
	// Writes first, deletions last, so a failed write leaves nothing gone.
	sort.SliceStable(pl.files, func(i, j int) bool { return !pl.files[i].delete && pl.files[j].delete })
	for _, f := range pl.files {
		var werr error
		switch {
		case f.delete && !f.existed:
			continue // added and deleted within the patch: never on disk
		case f.delete:
			werr = host.Remove(ctx, f.path)
		default:
			var data []byte
			data, werr = encodeChecked(f.after, f.form, "the patched "+f.display, "split the patch into smaller ones")
			if werr == nil {
				werr = mkdirAllOf(ctx, t.WS, filepath.Dir(f.path), 0o755)
			}
			if werr == nil {
				werr = writeFileOf(ctx, t.WS, f.path, data)
			}
		}
		if werr != nil {
			rollback()
			return "", fmt.Errorf("writing %s failed (%v); the files already written were restored", f.display, werr)
		}
		written = append(written, f)
	}
	var out []string
	for _, f := range written {
		t.Cache.Drop(f.path)
		if f.delete {
			t.Seen.Forget(f.path)
			out = append(out, "Deleted "+f.display)
			continue
		}
		t.Seen.Record(f.path, encodeText(f.after, f.form))
		a, r := diff.Stats(f.before, f.after)
		verb := "Updated"
		if !f.existed {
			verb = "Added"
		}
		out = append(out, fmt.Sprintf("%s %s (+%d -%d lines)", verb, f.display, a, r))
	}
	for _, n := range pl.notes {
		out = append(out, "["+n+"]")
	}
	return strings.Join(out, "\n"), nil
}

// patchPaths are the paths the patch names, unresolved, for the gates.
func patchPaths(input json.RawMessage) []string {
	var in patchInput
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(in.Input, "\n") {
		l = strings.TrimSpace(l)
		for _, p := range []string{"*** Add File:", "*** Delete File:", "*** Update File:", "*** Move to:"} {
			if rest, ok := strings.CutPrefix(l, p); ok && strings.TrimSpace(rest) != "" {
				out = append(out, strings.TrimSpace(rest))
			}
		}
	}
	return out
}

// Guard asks, whatever the mode, when any file is one of janetcode's own
// control surfaces, as edit_file does.
func (t ApplyPatch) Guard(input json.RawMessage) (bool, string) {
	for _, p := range patchPaths(input) {
		b, _ := json.Marshal(map[string]string{"path": p})
		if ask, why := guardSelfModify(rootOf(t.Root, t.WS), b); ask {
			return ask, why
		}
	}
	return false, ""
}

// Touches reports every file the patch will write or remove.
func (t ApplyPatch) Touches(input json.RawMessage) ([]string, error) {
	var out []string
	for _, p := range patchPaths(input) {
		abs, err := resolveWriteOn(t.WS, t.Root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, abs)
	}
	return out, nil
}

// Preview is a unified diff per file.
func (t ApplyPatch) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	pl, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range pl.files {
		after := f.after
		if f.delete {
			after = ""
		}
		b.WriteString(diff.Unified(f.display, f.before, after, 3))
	}
	return b.String(), nil
}
