package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/gitship"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The rest of /checkpoints, beside the list (see checkpointlist.go):
//
//	/checkpoints diff <a> [<b>|now]          compare
//	/checkpoints export <a> [<b>|now] [path] write the comparison as a patch
//	/checkpoints commit <n> [message]        preview a git commit of turn n
//	/checkpoints commit apply                make that commit
//
// A checkpoint is the state BEFORE its turn. "diff 3" is what turn 3
// itself did: from before it to before the next checkpoint, or to the
// work tree when it is the newest. "diff 3 5" runs from before turn 3 to
// before turn 5, and "diff 3 now" from before turn 3 to the work tree,
// which is what restoring to before turn 3 would undo. Restoring is
// still /undo turn <n>; nothing here changes the tree or the history
// except the commit, which only "commit apply" makes.

// maxCheckpointSheet bounds the patch a sheet carries; export writes it
// whole.
const maxCheckpointSheet = 60000

// checkpointUsage documents the shape, for every front-end.
const checkpointUsage = "usage: /checkpoints [diff <a> [<b>|now] | export <a> [<b>|now] [path] | commit <n> [message] | commit apply]"

// cpSpan is a parsed comparison: from before turn From to before turn To,
// to the work tree (Now), or, with neither, to the end of turn From.
type cpSpan struct {
	From, To int
	Now      bool
}

// cpCommand is a parsed "/checkpoints <verb> ..." line.
type cpCommand struct {
	verb    string // diff, export or commit
	span    cpSpan
	path    string // export's destination, "" for the default
	message string // commit's message, "" for the default
	apply   bool   // "commit apply"
}

var turnRef = regexp.MustCompile(`^t?(\d+)$`)

func parseTurn(s string) (int, bool) {
	m := turnRef.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

// parseCheckpointArgs parses the text after "/checkpoints" when it names
// a verb; empty is the list, which the front-ends draw themselves.
func parseCheckpointArgs(arg string) (cpCommand, error) {
	f := strings.Fields(arg)
	if len(f) == 0 {
		return cpCommand{}, errors.New(checkpointUsage)
	}
	c := cpCommand{verb: f[0]}
	rest := f[1:]
	switch c.verb {
	case "diff", "export":
		if len(rest) == 0 {
			return c, errors.New(checkpointUsage)
		}
		from, ok := parseTurn(rest[0])
		if !ok {
			return c, fmt.Errorf("%q is not a checkpoint: name a turn number as /checkpoints lists them\n%s", rest[0], checkpointUsage)
		}
		c.span.From = from
		rest = rest[1:]
		if len(rest) > 0 {
			if rest[0] == "now" {
				c.span.Now = true
				rest = rest[1:]
			} else if to, ok := parseTurn(rest[0]); ok {
				c.span.To = to
				rest = rest[1:]
			}
		}
		if c.verb == "diff" && len(rest) > 0 {
			return c, errors.New(checkpointUsage)
		}
		c.path = strings.Join(rest, " ")
	case "commit":
		if len(rest) == 1 && rest[0] == "apply" {
			c.apply = true
			return c, nil
		}
		if len(rest) == 0 {
			return c, errors.New(checkpointUsage)
		}
		n, ok := parseTurn(rest[0])
		if !ok {
			return c, fmt.Errorf("%q is not a checkpoint: name a turn number as /checkpoints lists them\n%s", rest[0], checkpointUsage)
		}
		c.span.From = n
		// The message is the rest of the line as typed, spaces and all.
		_, after, _ := strings.Cut(strings.TrimSpace(arg), f[1])
		c.message = strings.TrimSpace(after)
	default:
		return c, fmt.Errorf("unknown checkpoints action %q\n%s", c.verb, checkpointUsage)
	}
	return c, nil
}

// CheckpointCommand runs "/checkpoints diff|export|commit ..." for every
// front-end, shown relative to cwd. The bare list is the front-ends' own.
func (a *Agent) CheckpointCommand(ctx context.Context, cwd, arg string) (cmdresult.Result, error) {
	c, err := parseCheckpointArgs(arg)
	if err != nil {
		return cmdresult.Result{Error: err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitship.Timeout)
	defer cancel()
	switch c.verb {
	case "diff":
		p, err := a.checkpointPatch(ctx, c.span)
		if err != nil {
			return cmdresult.Result{Error: "checkpoints diff: " + err.Error()}, nil
		}
		return cmdresult.Result{Sheet: formatCheckpointPatch(p, cwd), SheetTitle: "checkpoint diff"}, nil
	case "export":
		return a.exportCheckpoint(ctx, cwd, c)
	default:
		if c.apply {
			return a.applyCheckpointCommit(ctx)
		}
		return a.previewCheckpointCommit(ctx, cwd, c.span.From, c.message)
	}
}

// CheckpointPatch is a comparison between two states of the work tree.
type CheckpointPatch struct {
	// From and To say what was compared, in words.
	From, To string
	// Patch is a diffstat then a unified patch that git apply accepts;
	// empty when the two states are the same.
	Patch string
	// Files are the paths it changes.
	Files []string
	// Note carries a caveat the reader needs, such as what a file-copy
	// checkpoint cannot see.
	Note string
}

// sortedCheckpoints is a copy of the checkpoints, oldest turn first.
func (k *checkpoints) sortedCheckpoints() []Checkpoint {
	k.cp.mu.Lock()
	cps := append([]Checkpoint(nil), k.cp.all...)
	k.cp.mu.Unlock()
	sort.SliceStable(cps, func(i, j int) bool { return cps[i].Turn < cps[j].Turn })
	return cps
}

func (k *checkpoints) restorableTurnsText(cps []Checkpoint) string {
	var names []string
	for _, cp := range cps {
		if cp.Turn > 0 {
			names = append(names, strconv.Itoa(cp.Turn))
		}
	}
	if len(names) == 0 {
		return "no turn has a checkpoint yet"
	}
	return "checkpoints exist for turns " + strings.Join(names, ", ")
}

// patcher is what a snapshot store needs to compare; the fakes tests use
// for failure injection do not, and say so.
type patcher interface {
	Patch(ctx context.Context, from snapshot.Snap, to *snapshot.Snap) (string, error)
}

var diffGitFile = regexp.MustCompile(`(?m)^diff --git a/(.+) b/(.+)$`)

// checkpointPatch resolves span against the session's checkpoints and
// compares the two states.
func (a *Agent) checkpointPatch(ctx context.Context, span cpSpan) (CheckpointPatch, error) {
	cps := a.sortedCheckpoints()
	var from *Checkpoint
	var next *Checkpoint // the first checkpoint after from
	var to *Checkpoint   // the one span.To names
	for i := range cps {
		cp := &cps[i]
		switch {
		case cp.Turn == span.From:
			from = cp
		case from != nil && next == nil && cp.Turn > span.From:
			next = cp
		}
		if span.To > 0 && cp.Turn == span.To {
			to = cp
		}
	}
	if from == nil {
		return CheckpointPatch{}, fmt.Errorf("no checkpoint for turn %d: %s", span.From, a.restorableTurnsText(cps))
	}
	out := CheckpointPatch{From: fmt.Sprintf("before turn %d", from.Turn)}
	end := next // nil: the work tree
	switch {
	case span.Now:
		end = nil
	case span.To > 0:
		if span.To <= span.From {
			return CheckpointPatch{}, fmt.Errorf("turn %d is not after turn %d", span.To, span.From)
		}
		if to == nil {
			return CheckpointPatch{}, fmt.Errorf("no checkpoint for turn %d: %s", span.To, a.restorableTurnsText(cps))
		}
		end = to
	}
	if end != nil {
		out.To = fmt.Sprintf("before turn %d", end.Turn)
	} else {
		out.To = "the work tree now"
	}
	repo := a.snaps()
	if from.Snap != nil && repo != nil {
		p, ok := repo.(patcher)
		if !ok {
			return CheckpointPatch{}, errors.New("this snapshot store cannot compare states")
		}
		var toSnap *snapshot.Snap
		if end != nil {
			if end.Snap == nil {
				return CheckpointPatch{}, fmt.Errorf("turn %d has no git snapshot (only file copies), so it cannot be compared with another checkpoint", end.Turn)
			}
			toSnap = end.Snap
		}
		patch, err := p.Patch(ctx, *from.Snap, toSnap)
		if err != nil {
			return CheckpointPatch{}, err
		}
		out.Patch = patch
	} else {
		// No snapshot: only the copies the file tools made, and only
		// against the work tree as it is.
		if end != nil {
			return CheckpointPatch{}, fmt.Errorf("turn %d has only file copies, which can be compared with the work tree (/checkpoints diff %d now) but not with another checkpoint", from.Turn, from.Turn)
		}
		out.Patch, out.Note = copiesPatch(a.displayRoot(), from.Files)
	}
	out.Files = patchFiles(out.Patch)
	return out, nil
}

// patchFiles lists the paths a patch changes, in the order it names them.
func patchFiles(patch string) []string {
	var files []string
	seen := map[string]bool{}
	for _, m := range diffGitFile.FindAllStringSubmatch(patch, -1) {
		if !seen[m[2]] {
			seen[m[2]] = true
			files = append(files, m[2])
		}
	}
	return files
}

// copiesPatch compares the file tools' copies from before a turn with
// the files as they are now. It sees only those files: a shell command's
// changes outside them are exactly what a checkpoint without a snapshot
// does not hold, and the note says so.
func copiesPatch(root string, files []Snapshot) (patch, note string) {
	var sb strings.Builder
	var skipped []string
	for _, f := range files {
		rel := textfmt.RelTo(root, f.Path)
		if f.Failed || f.TooLarge {
			skipped = append(skipped, rel)
			continue
		}
		before := string(f.Data)
		if f.Blob != "" {
			data, err := os.ReadFile(f.Blob)
			if err != nil {
				skipped = append(skipped, rel)
				continue
			}
			before = string(data)
		}
		now, err := os.ReadFile(f.Path)
		switch {
		case err != nil && !errors.Is(err, os.ErrNotExist):
			skipped = append(skipped, rel)
			continue
		case err != nil && !f.Existed:
			continue // created and gone again: no change
		case err != nil:
			fmt.Fprintf(&sb, "diff --git a/%s b/%s\ndeleted file mode 100644\n", rel, rel)
			sb.WriteString(diff.Unified(rel, before, "", 3))
			continue
		}
		if !f.Existed {
			fmt.Fprintf(&sb, "diff --git a/%s b/%s\nnew file mode 100644\n", rel, rel)
			sb.WriteString(diff.Unified(rel, "", string(now), 3))
			continue
		}
		if u := diff.Unified(rel, before, string(now), 3); u != "" {
			fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", rel, rel)
			sb.WriteString(u)
		}
	}
	note = "file copies only: changes a shell command made outside the files the file tools touched are not here"
	if len(skipped) > 0 {
		note += "; not comparable (not captured or too large): " + strings.Join(skipped, ", ")
	}
	return sb.String(), note
}

// formatCheckpointPatch renders a comparison for a sheet.
func formatCheckpointPatch(p CheckpointPatch, root string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s → %s\n", p.From, p.To)
	if p.Note != "" {
		sb.WriteString("note: " + p.Note + "\n")
	}
	if p.Patch == "" {
		sb.WriteString("\nno difference: the two states are the same\n")
		return strings.TrimRight(sb.String(), "\n")
	}
	sb.WriteString("\n")
	patch := p.Patch
	cut := false
	if len(patch) > maxCheckpointSheet {
		patch, cut = patch[:maxCheckpointSheet], true
	}
	sb.WriteString(strings.TrimRight(patch, "\n"))
	if cut {
		sb.WriteString("\n[cut here: /checkpoints export writes all of it]")
	}
	sb.WriteString("\n\n/checkpoints export writes this as a patch git apply takes · /undo turn <n> previews putting the tree back")
	return sb.String()
}

// exportCheckpoint writes the comparison to a file as a patch. It will
// not overwrite a file: an export is a record, and a name that is taken
// is the user's to resolve.
func (a *Agent) exportCheckpoint(ctx context.Context, cwd string, c cpCommand) (cmdresult.Result, error) {
	p, err := a.checkpointPatch(ctx, c.span)
	if err != nil {
		return cmdresult.Result{Error: "checkpoints export: " + err.Error()}, nil
	}
	if p.Patch == "" {
		return cmdresult.Result{Error: "checkpoints export: no difference between " + p.From + " and " + p.To + ", so there is nothing to write"}, nil
	}
	path := c.path
	if path == "" {
		name := fmt.Sprintf("janetcode-checkpoint-t%d", c.span.From)
		if c.span.To > 0 {
			name += fmt.Sprintf("-t%d", c.span.To)
		} else if c.span.Now {
			name += "-now"
		}
		path = name + ".patch"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	if _, err := os.Lstat(path); err == nil {
		return cmdresult.Result{Error: "checkpoints export: " + textfmt.RelTo(cwd, path) + " already exists; name another file: /checkpoints export " + strconv.Itoa(c.span.From) + " <path>"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return cmdresult.Result{Error: "checkpoints export: " + err.Error()}, nil
	}
	if err := fsutil.AtomicWrite(path, []byte(p.Patch)); err != nil {
		return cmdresult.Result{Error: "checkpoints export: " + err.Error()}, nil
	}
	return cmdresult.Result{
		Notes: []string{fmt.Sprintf("exported %s → %s (%s) to %s", p.From, p.To, textfmt.Plural(len(p.Files), "file"), textfmt.RelTo(cwd, path))},
		Tips:  []string{"git apply " + textfmt.RelTo(cwd, path) + " replays it on a tree as it was " + p.From},
	}, nil
}

// checkpointCommit is a previewed commit of one checkpoint's turn, held
// until "commit apply".
type checkpointCommit struct {
	turn    int
	message string
	files   []string // relative to root, as the preview listed them
	root    string
}

// ErrNoPendingCheckpointCommit is "commit apply" with nothing previewed.
var ErrNoPendingCheckpointCommit = errors.New("no previewed checkpoint commit: run /checkpoints commit <n> first")

// checkpointTurnPaths are the paths turn n's checkpoint says the turn
// changed: its manifest when it has one (which covers shell changes
// too), else the file tools' copies.
func checkpointTurnPaths(cp Checkpoint) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if cp.Manifested {
		for _, tf := range cp.Touched {
			add(tf.Path)
		}
		return out
	}
	for _, f := range cp.Files {
		add(f.Path)
	}
	return out
}

// attributionLines say what a checkpoint commit does about authorship,
// before anything is created: the user wants to know whether history
// will name janetcode or the model.
func (a *Agent) attributionLines() []string {
	lines := []string{
		"author and committer: your own git identity (user.name / user.email), exactly as for a commit you made yourself",
		"trailers: none are added — no Co-Authored-By, no 'generated by' line; the message is the one shown above",
	}
	if !a.Off["proof"] && a.Settings().ProofNotes {
		lines = append(lines, "proof note: one is attached as a git note (refs/notes, not part of the commit) naming the session, model and intents; /proof shows it")
	} else {
		lines = append(lines, "proof note: none (proof notes are off)")
	}
	return lines
}

// previewCheckpointCommit shows what committing turn n's work would do —
// files, message, attribution — and creates nothing.
func (a *Agent) previewCheckpointCommit(ctx context.Context, cwd string, turn int, message string) (cmdresult.Result, error) {
	if a.running.Load() {
		return cmdresult.Result{Error: "checkpoints commit: " + ErrTurnRunning.Error()}, nil
	}
	var cp *Checkpoint
	cps := a.sortedCheckpoints()
	for i := range cps {
		if cps[i].Turn == turn {
			cp = &cps[i]
		}
	}
	if cp == nil {
		return cmdresult.Result{Error: fmt.Sprintf("checkpoints commit: no checkpoint for turn %d: %s", turn, a.restorableTurnsText(cps))}, nil
	}
	plan, err := gitship.PlanCommit(ctx, cwd, checkpointTurnPaths(*cp))
	if err != nil {
		return cmdresult.Result{Error: "checkpoints commit: " + err.Error()}, nil
	}
	if len(plan.Files) == 0 {
		res := cmdresult.Result{Error: fmt.Sprintf("checkpoints commit: nothing to commit for turn %d: its files have no uncommitted changes", turn)}
		if len(plan.Withheld) > 0 {
			res.Error = fmt.Sprintf("checkpoints commit: nothing to commit for turn %d: the only changed files look like credentials", turn)
		}
		res.Tips = withheldTips(plan.Withheld)
		return res, nil
	}
	if message == "" {
		intent := ""
		for _, g := range a.ChangeGroups() {
			if g.Turn == turn && strings.TrimSpace(g.Intent) != "" {
				intent = strings.TrimSpace(g.Intent)
				break
			}
		}
		message = fmt.Sprintf("checkpoint t%d", turn)
		if intent != "" {
			message += ": " + intent
		}
	}
	a.cp.mu.Lock()
	a.pendingCommit = &checkpointCommit{turn: turn, message: message, files: plan.Files, root: plan.Root}
	a.cp.mu.Unlock()

	var sb strings.Builder
	fmt.Fprintf(&sb, "committing turn %d's work would create ONE commit on the current branch with:\n\n", turn)
	sb.WriteString("message:\n  " + strings.ReplaceAll(message, "\n", "\n  ") + "\n\n")
	fmt.Fprintf(&sb, "%s (only these are staged; everything else stays as it is):\n", textfmt.Plural(len(plan.Files), "file"))
	for _, f := range plan.Files {
		sb.WriteString("  " + f + "\n")
	}
	if len(plan.Others) > 0 {
		fmt.Fprintf(&sb, "\nleft out, not part of this turn: %s\n", textfmt.Plural(len(plan.Others), "other changed file"))
	}
	sb.WriteString("\nattribution:\n")
	for _, l := range a.attributionLines() {
		sb.WriteString("  " + l + "\n")
	}
	sb.WriteString("\nnote: a commit moves HEAD, after which a snapshot restore of an earlier checkpoint is refused (file copies cover only file-tool edits), as after /commit\n")
	sb.WriteString("\n/checkpoints commit apply makes the commit; nothing has been created yet")
	return cmdresult.Result{Sheet: sb.String(), SheetTitle: "checkpoint commit", Tips: withheldTips(plan.Withheld)}, nil
}

// applyCheckpointCommit makes the commit the last preview showed. The
// plan is made again, and a different file list than the one the user
// saw is refused rather than committed.
func (a *Agent) applyCheckpointCommit(ctx context.Context) (cmdresult.Result, error) {
	if a.running.Load() {
		return cmdresult.Result{Error: "checkpoints commit: " + ErrTurnRunning.Error()}, nil
	}
	a.cp.mu.Lock()
	p := a.pendingCommit
	a.cp.mu.Unlock()
	if p == nil {
		return cmdresult.Result{Error: "checkpoints commit: " + ErrNoPendingCheckpointCommit.Error()}, nil
	}
	cps := a.sortedCheckpoints()
	var cp *Checkpoint
	for i := range cps {
		if cps[i].Turn == p.turn {
			cp = &cps[i]
		}
	}
	if cp == nil {
		return cmdresult.Result{Error: fmt.Sprintf("checkpoints commit: the checkpoint for turn %d is gone; preview again", p.turn)}, nil
	}
	plan, err := gitship.PlanCommit(ctx, p.root, checkpointTurnPaths(*cp))
	if err != nil {
		return cmdresult.Result{Error: "checkpoints commit: " + err.Error()}, nil
	}
	if strings.Join(plan.Files, "\x00") != strings.Join(p.files, "\x00") {
		return cmdresult.Result{Error: "checkpoints commit: the files changed since the preview; run /checkpoints commit " + strconv.Itoa(p.turn) + " again to see what would be committed"}, nil
	}
	sha, err := gitship.Commit(ctx, plan.Root, plan.Files, p.message)
	if err != nil {
		return cmdresult.Result{Error: "checkpoints commit: " + err.Error()}, nil
	}
	a.cp.mu.Lock()
	a.pendingCommit = nil
	a.cp.mu.Unlock()
	subject, _, _ := strings.Cut(p.message, "\n")
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("committed %s · %s · %s", short(sha), subject, textfmt.Plural(len(plan.Files), "file"))}}
	if !a.Off["proof"] && a.Settings().ProofNotes {
		if sum, perr := a.AttachProof(ctx, plan.Root, sha); perr == nil && sum != "" {
			res.Notes = append(res.Notes, "proof attached · /proof shows it")
		}
	}
	return res, nil
}
