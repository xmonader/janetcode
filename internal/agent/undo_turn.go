// Restoring the work tree to how it stood before a turn: "/undo turn n".
// Plain /undo walks back one turn at a time; this jumps straight to the
// state a turn started from, out of the checkpoints the turn loop already
// takes (see undo.go), and is a preview the same "/undo apply" carries
// out — so there is one confirmation mechanism, not two.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// ErrNoTurnCheckpoint is returned when the snapshot for a turn is not
// there to restore: the turn changed nothing, or it is old enough that
// trimming dropped it (see maxCheckpoints). The message names the turns
// that can still be restored.
var ErrNoTurnCheckpoint = errors.New("no snapshot for that turn")

// ErrNothingToRestore is returned when the session has no checkpoint at
// all, so no turn can be restored to.
var ErrNothingToRestore = errors.New("no turn in this session has changed anything yet, so there is nothing to restore to")

// TurnRestore is what "/undo turn n" would do: the turn it goes back to,
// when that turn started, and one "verb path" line per file, the same
// wording UndoPreview uses.
type TurnRestore struct {
	Turn  int
	When  time.Time
	Lines []string
}

// RestorableTurns lists the turns whose "before" state is still on hand,
// oldest first — what /changes marks and "/undo turn n" accepts.
func (k *checkpoints) RestorableTurns() []int {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	var out []int
	for _, cp := range k.cp.all {
		if cp.Turn > 0 {
			out = append(out, cp.Turn)
		}
	}
	sort.Ints(out)
	return out
}

// checkpointForTurn is the checkpoint holding the state before turn n.
// Called with cp.mu held.
func (k *checkpoints) checkpointForTurn(n int) (Checkpoint, bool) {
	for _, cp := range k.cp.all {
		if cp.Turn == n {
			return cp, true
		}
	}
	return Checkpoint{}, false
}

// turnFingerprint is the checkpoint stack as it stands, so an apply can
// tell that the turns moved under a preview (another turn ran, an /undo
// popped one, trimming dropped the oldest) and refuse rather than
// restoring a state the user never saw listed. Called with cp.mu held.
func (k *checkpoints) turnFingerprint() string {
	var sb strings.Builder
	for _, cp := range k.cp.all {
		sb.WriteString(strconv.Itoa(cp.Turn))
		if cp.Snap != nil {
			sb.WriteString(":" + cp.Snap.Commit)
		}
		sb.WriteString(",")
	}
	return sb.String()
}

// noTurnCheckpoint explains which turns can be restored instead of n.
// Called with cp.mu held.
func (k *checkpoints) noTurnCheckpoint(n int) error {
	var turns []int
	for _, cp := range k.cp.all {
		if cp.Turn > 0 {
			turns = append(turns, cp.Turn)
		}
	}
	sort.Ints(turns)
	if len(turns) == 0 {
		return ErrNothingToRestore
	}
	var names []string
	for _, t := range turns {
		names = append(names, strconv.Itoa(t))
	}
	return fmt.Errorf("%w: turn %d either changed nothing or is old enough that its snapshot was trimmed (the last %d turns that changed something keep one); restorable turns: %s", ErrNoTurnCheckpoint, n, maxCheckpoints, strings.Join(names, ", "))
}

// PreviewUndoTurn lists what putting the work tree back to before turn n
// would change, and remembers it as the session's pending undo for a
// following ApplyPendingUndo. Nothing is touched.
func (a *Agent) PreviewUndoTurn(ctx context.Context, n int) (TurnRestore, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	cp, ok := a.checkpointForTurn(n)
	if !ok {
		return TurnRestore{}, a.noTurnCheckpoint(n)
	}
	tr := TurnRestore{Turn: n, When: cp.When}
	repo := a.snaps()
	if cp.Snap != nil && repo != nil {
		mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		moved, err := repo.Moved(mctx, *cp.Snap)
		cancel()
		if err != nil {
			return TurnRestore{}, err
		}
		if moved {
			// Restoring the snapshot now would mix the tree it recorded
			// with commits made since, so say so instead of listing a
			// diff against a state that is no longer comparable.
			return TurnRestore{}, fmt.Errorf("%w: the repository moved since turn %d was snapshotted (a commit, checkout or reset), so its work tree cannot be put back", snapshot.ErrHeadMoved, n)
		}
	}
	lines, err := a.checkpointPreview(ctx, cp)
	if err != nil {
		return TurnRestore{}, err
	}
	tr.Lines = lines
	a.pendingUndo = &pendingUndo{fingerprint: a.turnFingerprint(), turn: &tr, cp: cp}
	return tr, nil
}

// applyTurnRestore carries out a previewed turn restore: the tree goes
// back to the checkpoint, every file it touched is recorded as a change
// (so the restore is itself undoable, and shows up in /changes and
// /blame like any other edit), and the checkpoints of that turn and
// everything after it are dropped, since the states they describe are no
// longer ahead of the tree. Called with cp.mu held; it unlocks around
// the work the way Undo does.
func (a *Agent) applyTurnRestore(ctx context.Context, tr TurnRestore, cp Checkpoint) (UndoReport, error) {
	paths := previewPaths(tr.Lines)
	before := make(map[string][]byte, len(paths))
	existed := make(map[string]bool, len(paths))
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			before[p], existed[p] = data, true
		}
	}
	repo := a.snaps()
	var (
		done []string
		err  error
	)
	if cp.Snap != nil && repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		done, err = repo.Restore(rctx, *cp.Snap)
		cancel()
		if errors.Is(err, snapshot.ErrHeadMoved) {
			// The file copies cover only what the file tools touched, so
			// applying them would half-restore the tree and call it a
			// restore. Refuse instead, with nothing changed.
			return UndoReport{}, fmt.Errorf("%w: the repository moved since turn %d was snapshotted, so its work tree cannot be put back", err, tr.Turn)
		}
	} else {
		done, err = a.restore(ctx, cp)
	}
	if err != nil {
		return UndoReport{}, err
	}
	a.dropFromTurn(ctx, tr.Turn)
	a.cp.dirty = true
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	root := a.displayRoot()
	var rep UndoReport
	for _, p := range paths {
		now, _ := os.ReadFile(p)
		a.record(Change{Turn: a.Stats().Turns, Tool: "undo", Intent: fmt.Sprintf("restore the work tree to before turn %d", tr.Turn), Path: p, Existed: existed[p], Before: before[p], After: now})
	}
	for _, d := range done {
		verb, path, ok := strings.Cut(d, " ")
		if !ok {
			rep.Reverted = append(rep.Reverted, d)
			continue
		}
		rep.Reverted = append(rep.Reverted, verb+" "+textfmt.RelTo(root, path))
	}
	sort.Strings(rep.Reverted)
	if len(rep.Reverted) == 0 {
		rep.Reverted = append(rep.Reverted, fmt.Sprintf("the work tree already matched its state before turn %d", tr.Turn))
	}
	a.reportChecks(ctx, &rep, Selection{})
	a.cp.mu.Lock()
	return rep, nil
}

// dropFromTurn forgets the checkpoints of turn n and everything after
// it: the tree no longer holds those turns' changes, so their "before"
// states describe a future that was just thrown away, and a later /undo
// aiming at one would put the changes back rather than take more away.
// Called with cp.mu held.
func (a *Agent) dropFromTurn(ctx context.Context, n int) {
	ctx = context.WithoutCancel(ctx)
	repo := a.snaps()
	var keep []Checkpoint
	for _, cp := range a.cp.all {
		if cp.Turn >= n && cp.Turn > 0 {
			if repo != nil && cp.Snap != nil {
				repo.Drop(ctx, *cp.Snap)
			}
			continue
		}
		keep = append(keep, cp)
	}
	a.cp.all = keep
	a.cp.open = false
	a.cp.dropRedo(ctx, repo)
}

// checkpointPreview lists what restoring a checkpoint would change, one
// "verb path" line per file. Called with cp.mu held.
func (a *Agent) checkpointPreview(ctx context.Context, cp Checkpoint) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	if repo := a.snaps(); cp.Snap != nil && repo != nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		diff, err := repo.Diff(ctx, *cp.Snap)
		cancel()
		if err != nil {
			return nil, err
		}
		for _, d := range diff {
			status, path, _ := strings.Cut(d, " ")
			seen[path] = true
			switch status {
			case "A":
				out = append(out, "remove "+path)
			case "D":
				out = append(out, "recreate "+path)
			default:
				out = append(out, "restore "+path)
			}
		}
	}
	for _, f := range cp.Files {
		if seen[f.Path] {
			continue
		}
		if f.Failed {
			out = append(out, "cannot restore "+f.Path+" (not captured)")
			continue
		}
		if f.Existed {
			out = append(out, "restore "+f.Path)
		} else {
			out = append(out, "remove "+f.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// previewPaths are the paths a preview's lines name, skipping the ones
// it already said it cannot restore.
func previewPaths(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "cannot restore ") {
			continue
		}
		if _, path, ok := strings.Cut(l, " "); ok {
			out = append(out, path)
		}
	}
	return out
}

// FormatTurnRestore renders a previewed turn restore for a front-end
// sheet: what it would do to each file, and how to carry it out.
func FormatTurnRestore(root string, tr TurnRestore) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "restoring the work tree to before turn %d", tr.Turn)
	if !tr.When.IsZero() {
		fmt.Fprintf(&sb, " (%s ago)", textfmt.Age(time.Since(tr.When)))
	}
	sb.WriteString(" would:\n")
	for _, l := range tr.Lines {
		verb, path, _ := strings.Cut(l, " ")
		fmt.Fprintf(&sb, "  %-9s %s\n", verb, textfmt.RelTo(root, path))
	}
	if len(tr.Lines) == 0 {
		sb.WriteString("  nothing: the tree already matches that state\n")
	}
	sb.WriteString("\n/undo apply carries it out; the restore is itself recorded as a change, so /undo <n> takes it back")
	return sb.String()
}

// UndoTurnTimeout bounds the git work "/undo turn n" does to list what
// it would change, in every front-end.
const UndoTurnTimeout = 2 * time.Minute

// UndoToTurn runs "/undo turn <n>" for every front-end: it previews
// putting the work tree back to before turn n, shown relative to root.
// Nothing is touched; "/undo apply" carries it out.
func (a *Agent) UndoToTurn(ctx context.Context, root string, n int) (cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, UndoTurnTimeout)
	defer cancel()
	tr, err := a.PreviewUndoTurn(ctx, n)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("undo turn %d: %w", n, err)
	}
	return cmdresult.Result{Sheet: FormatTurnRestore(root, tr), SheetTitle: "undo turn"}, nil
}
