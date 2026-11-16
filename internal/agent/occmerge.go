package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/occ"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// The merge-back of editing subagents (Agent.SubagentMerge). Without it
// an editing subagent's work comes back as a patch the main agent applies
// with git apply, and three of them started in one step all patch the
// same base: the second patch to land applies against a project the
// first one changed, and nothing says whether the second subagent's
// reasoning still holds there.
//
// With it, a finishing subagent's work is validated and merged by the
// harness (internal/occ decides, this file reads and writes): edits that
// commute with what changed meanwhile go straight into the project, with
// a checkpoint and a provenance record like any edit; what does not
// commute is handed back to the subagent once, with both sides' deltas,
// in a checkout brought up to date with the project. A second failure is
// returned to the main agent as a patch against the current project, the
// project untouched.

// patternLog is the greps and globs an editing subagent ran: the range
// reads of its read-set.
type patternLog struct {
	mu   sync.Mutex
	dir  string
	list []occ.PatternRead
}

func (l *patternLog) add(name string, input json.RawMessage) {
	var in struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Glob            string `json:"glob"`
		CaseInsensitive bool   `json:"case_insensitive"`
	}
	if json.Unmarshal(input, &in) != nil || in.Pattern == "" {
		return
	}
	p := in.Path
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(l.dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return // outside the checkout: nothing merge-back can check
		}
		p = rel
	}
	pr := occ.PatternRead{Tool: name, Pattern: in.Pattern, Path: filepath.ToSlash(filepath.Clean(p)), Glob: in.Glob, CaseInsensitive: in.CaseInsensitive}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, have := range l.list {
		if have == pr {
			return
		}
	}
	l.list = append(l.list, pr)
}

func (l *patternLog) reads() []occ.PatternRead {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]occ.PatternRead(nil), l.list...)
}

// rangeRead is grep or glob, recording each successful call in the log.
type rangeRead struct {
	tool.Tool
	log *patternLog
}

func (r rangeRead) Run(ctx context.Context, input json.RawMessage) (string, error) {
	out, err := r.Tool.Run(ctx, input)
	if err == nil {
		r.log.add(r.Tool.Name(), input)
	}
	return out, err
}

// baseOf is what the subagent's edits to rel are measured against: the
// content the merge-back last brought it to, else the checkout's base.
func (w *worktree) baseOf(ctx context.Context, rel string) occ.Version {
	if v, ok := w.rebased[rel]; ok {
		return v
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", w.base+":"+rel)
	cmd.Dir = w.Root
	out, err := cmd.Output()
	if err != nil {
		return occ.Version{}
	}
	return occ.Version{Data: out, Exists: true}
}

func readVersion(path string) occ.Version {
	data, err := os.ReadFile(path)
	if err != nil {
		return occ.Version{}
	}
	return occ.Version{Data: data, Exists: true}
}

// candidates are the paths the subagent may have changed: whatever git
// sees differ from the base commit, and every path rebased since.
func (w *worktree) candidates(ctx context.Context) ([]string, error) {
	if out, err := gitRunEnv(ctx, w.Dir, w.raw, "add", "-A", "--", "."); err != nil {
		return nil, fmt.Errorf("git add: %v: %s", err, out)
	}
	out, err := gitRunEnv(ctx, w.Dir, w.raw, "diff", "--cached", "--name-only", "--no-renames", "-z", w.base)
	if err != nil {
		return nil, fmt.Errorf("git diff: %v: %s", err, out)
	}
	return w.withRebased(strings.Split(out, "\x00")), nil
}

// movedInProject are the paths the project changed since the base
// snapshot, plus every path rebased since (its base moved with it).
func (w *worktree) movedInProject(ctx context.Context) ([]string, error) {
	lines, err := w.repo.Diff(ctx, *w.snap)
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, l := range lines {
		if len(l) < 3 {
			continue
		}
		if rel, err := filepath.Rel(w.Root, l[2:]); err == nil && !strings.HasPrefix(rel, "..") {
			rels = append(rels, filepath.ToSlash(rel))
		}
	}
	return w.withRebased(rels), nil
}

func (w *worktree) withRebased(rels []string) []string {
	set := map[string]bool{}
	for _, r := range rels {
		if r = strings.TrimSpace(r); r != "" {
			set[r] = true
		}
	}
	for r := range w.rebased {
		set[r] = true
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// occInput reads the three sides of everything the subagent wrote, read
// and searched, and of everything the project changed meanwhile.
func (w *worktree) occInput(ctx context.Context) (occ.Input, error) {
	var in occ.Input
	cands, err := w.candidates(ctx)
	if err != nil {
		return in, err
	}
	written := map[string]bool{}
	for _, rel := range cands {
		f := occ.File{Path: rel, Base: w.baseOf(ctx, rel), Ours: readVersion(w.abs(w.Dir, rel)), Theirs: readVersion(w.abs(w.Root, rel))}
		if f.Ours.Same(f.Base) {
			continue
		}
		written[rel] = true
		in.Written = append(in.Written, f)
	}
	moved, err := w.movedInProject(ctx)
	if err != nil {
		return in, err
	}
	for _, rel := range moved {
		f := occ.File{Path: rel, Base: w.baseOf(ctx, rel), Theirs: readVersion(w.abs(w.Root, rel))}
		if !f.Theirs.Same(f.Base) {
			in.Moved = append(in.Moved, f)
		}
	}
	var reads []string
	for abs := range w.seen.Reads() {
		rel, err := filepath.Rel(w.Dir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if rel = filepath.ToSlash(rel); !written[rel] {
			reads = append(reads, rel)
		}
	}
	sort.Strings(reads)
	for _, rel := range reads {
		in.Read = append(in.Read, occ.File{Path: rel, Base: w.baseOf(ctx, rel), Theirs: readVersion(w.abs(w.Root, rel))})
	}
	in.Patterns = w.patterns.reads()
	return in, nil
}

func (w *worktree) abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// rebase brings the checkout up to date with the project before a repair
// round: every file the project changed takes the project's version, or
// the merge of both sides where the subagent's edits to it commute, and
// that version becomes the file's base. A file in conflict therefore
// holds the other side's version, and the subagent re-applies its own
// change from the delta it is shown.
func (w *worktree) rebase(in occ.Input, res occ.Result) {
	if w.rebased == nil {
		w.rebased = map[string]occ.Version{}
	}
	for _, f := range in.Moved {
		put := f.Theirs
		if v, ok := res.Apply[f.Path]; ok {
			put = v
		}
		w.put(f.Path, put)
		w.rebased[f.Path] = f.Theirs
	}
}

// settle records, after a merge, that the checkout and the project agree
// on every path the merge wrote or the project had changed: the next
// merge of a continued subagent measures from there.
func (w *worktree) settle(in occ.Input, res occ.Result) {
	if w.rebased == nil {
		w.rebased = map[string]occ.Version{}
	}
	for _, f := range in.Moved {
		if _, ok := res.Apply[f.Path]; !ok {
			w.put(f.Path, f.Theirs)
			w.rebased[f.Path] = f.Theirs
		}
	}
	for _, f := range in.Written {
		v, ok := res.Apply[f.Path]
		if !ok {
			v = f.Ours // the project already had the same change
		}
		w.put(f.Path, v)
		w.rebased[f.Path] = v
	}
}

// put writes v into the checkout at rel. The subagent's own record of the
// file is dropped: the harness changed it, so an edit is not refused as
// "changed on disk" (an edit names what it replaces and is checked by
// that), while a whole-file write still needs a fresh read. The read-set
// keeps the entry.
func (w *worktree) put(rel string, v occ.Version) {
	path := w.abs(w.Dir, rel)
	writeVersion(path, v, w.abs(w.Root, rel))
	w.seen.Forget(path)
}

// writeVersion puts v at path, keeping the mode of the file there, else
// of like, else 0644. A version that does not exist removes the file.
func writeVersion(path string, v occ.Version, like string) error {
	if !v.Exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	} else if st, err := os.Stat(like); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, v.Data, mode)
}

// mergeRefused says why the harness may not write these paths into the
// project on the session's behalf, or "" when it may: the session is
// read-only, a rule denies or insists on asking about a write there, the
// path is protected, or writing it would need an approval the session
// has not given in advance. Merge-back never prompts: a subagent whose
// work would have needed a yes is handed back as a patch, as before.
func (t *Task) mergeRefused(root string, paths []string) string {
	p := t.Parent
	ps := p.Settings()
	if ps.readOnly() {
		return "the session is read-only"
	}
	if why := t.mergeNotAdmitted(root, paths); why != "" {
		return why
	}
	rules := p.RulesSnapshot()
	for _, rel := range paths {
		input, _ := json.Marshal(map[string]string{"path": filepath.Join(root, filepath.FromSlash(rel))})
		c := perm.Call{Tool: "write_file", Input: input, Root: root}
		c.Subject = perm.SubjectIn("write_file", input, root)
		v, r, why := rules.DecideCall(c)
		switch {
		case v == perm.Denied && why != "":
			return "writing " + rel + " is refused: " + why
		case v == perm.Denied:
			return "writing " + rel + " is denied by " + r.String()
		case v == perm.Ask && r.Tool != "":
			return "writing " + rel + " always asks (" + r.String() + ")"
		case v == perm.Ask && !ps.AutoApprove && !ps.AutoEdits && !ps.AutoMode:
			return "writing " + rel + " would need your approval"
		}
		if p.Protected != nil && !p.Off["protected"] {
			if _, pat, ok := p.Protected.Writes(c); ok {
				return rel + " is a protected path (" + pat + ")"
			}
		}
	}
	return ""
}

// mergeTouch stands in for the merge as the tool call the checkpoint is
// taken before: it names the paths the merge is about to write.
type mergeTouch struct{ paths []string }

func (mergeTouch) Name() string            { return "task" }
func (mergeTouch) Description() string     { return "" }
func (mergeTouch) Mutating() bool          { return true }
func (mergeTouch) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (mergeTouch) Run(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("not a tool")
}
func (m mergeTouch) Touches(json.RawMessage) ([]string, error) { return m.paths, nil }

// applyMerge writes the merged result into the project the way an edit
// would land there: after the pre-edit check run, under the turn's
// checkpoint, with a provenance record per file.
func (t *Task) applyMerge(ctx context.Context, w *worktree, id, desc string, res occ.Result) error {
	p := t.Parent
	if run := p.baseline; run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
		}
	}
	owner := p
	if p.UndoOwner != nil {
		owner = p.UndoOwner
	}
	paths := make([]string, 0, len(res.Apply))
	for rel := range res.Apply {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	abs := make([]string, len(paths))
	for i, rel := range paths {
		abs[i] = w.abs(w.Root, rel)
	}
	owner.cp.snapshot(ctx, owner.snaps(), owner.blobDir, mergeTouch{paths: abs}, nil)
	for i, rel := range paths {
		before := readVersion(abs[i])
		after := res.Apply[rel]
		if err := writeVersion(abs[i], after, w.abs(w.Dir, rel)); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		owner.record(Change{Turn: p.turnN, Tool: "task", Intent: intentLine("merged from subagent "+id+": "+desc, 120),
			Path: abs[i], Existed: before.Exists, Before: before.Data, After: after.Data, Gone: !after.Exists, Attempt: p.Attempts.OpenFor(id)})
		p.logEvent(session.Event{Type: session.TypeFileChange, Name: abs[i], Arguments: contentDigest(before.Data, before.Exists) + "->" + contentDigest(after.Data, after.Exists)})
	}
	t.merged.Store(true)
	return nil
}

// noteSubagentMerges marks the turn as one that changed files when a
// subagent's work was merged into the project during the step just run,
// so the verify gate runs at its end as it would after an edit. The flag
// is set on the task's goroutine and read here, on the turn's.
func (a *Agent) noteSubagentMerges() {
	if tt := a.TaskTool(); tt != nil && tt.merged.Swap(false) {
		a.mutated = true
		a.mutations++
	}
}

// maxDeltaBytes bounds each diff shown in a repair brief or a report.
const maxDeltaBytes = 6000

// repairBrief is the message that sends the conflicts back to the
// subagent: what collided, both sides' deltas, and what its checkout now
// holds.
func repairBrief(res occ.Result) string {
	var b strings.Builder
	b.WriteString("While you worked, the project changed under you (another subagent's work, or the main agent's). The harness merged everything that commutes with it; these parts of your work did not:\n")
	for _, c := range res.Conflicts {
		fmt.Fprintf(&b, "\n### %s (%s)\n%s.\n", c.Path, c.Kind, c.Detail)
		if c.Ours != "" {
			b.WriteString("Your change:\n```diff\n" + textfmt.ClipBytes(c.Ours, maxDeltaBytes) + "\n```\n")
		}
		if c.Theirs != "" {
			b.WriteString("The other change:\n```diff\n" + textfmt.ClipBytes(c.Theirs, maxDeltaBytes) + "\n```\n")
		}
	}
	b.WriteString("\nYour checkout has been brought up to date with the project: every file the other side changed now holds its version, merged with your edits where they did not collide. A file listed above as an overlap holds the other version only, so re-apply what you meant on top of it (read it first). For a stale read or search, check that your edits still fit what changed, and adjust them if not. Then re-run the relevant checks and report again, briefly. This is the only repair round.")
	return b.String()
}

// filePatch is base→v for one file as a patch git apply takes.
func filePatch(rel string, base, v occ.Version) string {
	if base.Same(v) {
		return ""
	}
	from, to := "a/"+rel, "b/"+rel
	if !base.Exists {
		from = "/dev/null"
	}
	if !v.Exists {
		to = "/dev/null"
	}
	body := diff.Unified(rel, string(base.Data), string(v.Data), 3)
	if body == "" {
		return ""
	}
	_, hunks, _ := strings.Cut(body, "\n+++ "+rel+"\n")
	return "--- " + from + "\n+++ " + to + "\n" + hunks
}

// mergeBack is the merge-back of an editing subagent that has just
// finished. It returns the text for the task result in place of the
// patch, and whether it settled the result: false leaves the caller to
// return the patch the old way, with the text (a reason) in front.
func (t *Task) mergeBack(ctx context.Context, id string, kid *subagent, desc string, emit func(Event)) (string, bool) {
	w, child := kid.wt, kid.agent
	// One merge at a time: each reads the project the one before wrote.
	t.mergeMu.Lock()
	locked := true
	defer func() {
		if locked {
			t.mergeMu.Unlock()
		}
	}()
	wctx := context.WithoutCancel(ctx)
	in, err := w.occInput(wctx)
	if err != nil {
		return "[merge-back could not read the changes (" + err.Error() + "); the patch is returned instead]\n", false
	}
	if len(in.Written) == 0 {
		if len(w.rebased) > 0 {
			// A continued subagent's checkout holds what earlier merges
			// brought in: a diff against its base commit would hand that
			// back as if it were new.
			return "\n\n[the subagent changed nothing that is not already in the project]", true
		}
		return "", false
	}
	paths := make([]string, 0, len(in.Written))
	for _, f := range in.Written {
		paths = append(paths, f.Path)
	}
	if why := t.mergeRefused(w.Root, paths); why != "" {
		return "[merge-back not done: " + why + "; the patch is returned for you to apply]\n", false
	}
	res := occ.Check(in)
	first := res
	repaired := false
	if !res.OK() && ctx.Err() == nil {
		w.rebase(in, res)
		// The subagent repairs without the lock: the repair is a model
		// run, and other subagents may merge meanwhile. Whatever lands
		// is checked when it comes back.
		t.mergeMu.Unlock()
		locked = false
		t.mu.Lock()
		kid.done, kid.last = false, "repairing merge conflicts"
		t.mu.Unlock()
		rerr := child.Run(ctx, repairBrief(res), emit)
		t.mu.Lock()
		kid.done = true
		t.bill(kid, child)
		t.mu.Unlock()
		t.mergeMu.Lock()
		locked = true
		repaired = true
		if rerr != nil {
			return fmt.Sprintf("[merge-back: the repair round failed (%v); the project is untouched by this subagent]\n", rerr), true
		}
		if in, err = w.occInput(wctx); err != nil {
			return "[merge-back could not read the repaired changes (" + err.Error() + "); the project is untouched by this subagent]\n", true
		}
		// The repair round can write paths the first round did not, and
		// those are as much the harness's to refuse: checked only before
		// it, a file a deny rule refuses reached the project by way of a
		// conflict.
		paths = paths[:0]
		for _, f := range in.Written {
			paths = append(paths, f.Path)
		}
		if why := t.mergeRefused(w.Root, paths); why != "" {
			return "\n\n## Not merged: " + why + " (written in the repair round)\nThe project is untouched by this subagent." + unmergedPatch(in, desc), true
		}
		res = occ.Check(in)
	}
	t.Parent.logEvent(session.Event{Type: session.TypeNotice, Name: "subagent/merge", Output: mergeLog(id, first, res, repaired)})
	if !res.OK() {
		return conflictReport(in, res, desc, repaired), true
	}
	if len(res.Apply) > 0 {
		if err := t.applyMerge(wctx, w, id, desc, res); err != nil {
			return "[merge-back failed while writing (" + err.Error() + "); some files may be written — check git status]\n", true
		}
	}
	w.settle(in, res)
	return mergedReport(in, res, first, repaired), true
}

// mergeLog is the session-log line a benchmark counts conflicts by.
func mergeLog(id string, first, final occ.Result, repaired bool) string {
	s := fmt.Sprintf("%s: %d clean, %d merged, %d independent reads, %d conflicts", id, len(final.Clean), len(final.Merged), len(final.Independent), len(first.Conflicts))
	if repaired {
		s += fmt.Sprintf(", %d after one repair round", len(final.Conflicts))
	}
	return s
}

func mergedReport(in occ.Input, res, first occ.Result, repaired bool) string {
	var b strings.Builder
	b.WriteString("\n\n## Merged into the project by the harness\n")
	b.WriteString("These changes are already in the project; do not git apply them.\n")
	if len(res.Clean) > 0 {
		b.WriteString("- applied as the subagent left them: " + strings.Join(res.Clean, ", ") + "\n")
	}
	if len(res.Merged) > 0 {
		b.WriteString("- merged with changes made meanwhile (they touched other declarations): " + strings.Join(res.Merged, ", ") + "\n")
	}
	if len(res.Independent) > 0 {
		b.WriteString("- read by the subagent and changed meanwhile, in nothing its edits use: " + strings.Join(res.Independent, ", ") + "\n")
	}
	if repaired {
		fmt.Fprintf(&b, "- %s sent back to the subagent and resolved in one repair round\n", textfmt.Plural(len(first.Conflicts), "conflict"))
	}
	if len(res.Apply) == 0 {
		b.WriteString("- nothing was left to apply: the project already holds what the subagent's work comes to\n")
	}
	var patch strings.Builder
	for _, f := range in.Written {
		patch.WriteString(filePatch(f.Path, f.Base, f.Ours))
	}
	if patch.Len() > 0 {
		b.WriteString("\nWhat the subagent changed:\n```diff\n" + textfmt.Truncate(patch.String()) + "\n```")
	}
	return b.String()
}

func conflictReport(in occ.Input, res occ.Result, desc string, repaired bool) string {
	var b strings.Builder
	if repaired {
		b.WriteString("\n\n## Not merged: conflicts remain after one repair round\n")
	} else {
		b.WriteString("\n\n## Not merged: conflicts with changes made meanwhile\n")
	}
	for _, c := range res.Conflicts {
		fmt.Fprintf(&b, "- %s (%s): %s\n", c.Path, c.Kind, c.Detail)
	}
	b.WriteString("The project is untouched by this subagent.")
	return b.String() + unmergedPatch(in, desc)
}

// unmergedPatch is the subagent's work that was not merged, measured
// against the project as its checkout last saw it: saved to a file and
// shown, or "" when it changed nothing.
func unmergedPatch(in occ.Input, desc string) string {
	var patch strings.Builder
	for _, f := range in.Written {
		patch.WriteString(filePatch(f.Path, f.Base, f.Ours))
	}
	if patch.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" Its work, measured against the project as its checkout last saw it, is below")
	if path, err := savePatch(patch.String(), desc); err == nil {
		b.WriteString(" and saved to " + path + " — resolve the conflicts above, then apply what still fits with: git apply " + path)
	}
	b.WriteString("\n\n```diff\n" + textfmt.Truncate(patch.String()) + "\n```")
	return b.String()
}
