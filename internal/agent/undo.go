package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// Snapshot is a file as it was before a turn changed it.
type Snapshot struct {
	Path    string `json:"path"`
	Data    []byte `json:"data,omitempty"`
	Existed bool   `json:"existed"`
	// Failed marks a file whose state before the turn could not be read
	// — a permission, a device error, something that is not a regular
	// file. Restoring the empty copy would write over the real content
	// with nothing and call it an undo, so the file is left alone and
	// the failure is reported instead.
	Failed bool `json:"failed,omitempty"`
}

// Checkpoint is the state before a turn that changed something. Inside a
// git repository Snap holds the whole work tree (so shell changes revert
// too); Files are the copies the file tools made, used outside git or when
// the snapshot cannot be restored.
type Checkpoint struct {
	When  time.Time      `json:"when"`
	Files []Snapshot     `json:"files"`
	Snap  *snapshot.Snap `json:"snap,omitempty"`
	// Turn is the turn the checkpoint belongs to and Messages how many
	// messages the conversation held when it began, so an undo can take
	// the turn's messages out of the context along with its file changes.
	Turn     int `json:"turn,omitempty"`
	Messages int `json:"messages,omitempty"`
}

// maxCheckpoints bounds how many turns can be undone; older ones are
// forgotten and their git refs dropped.
const maxCheckpoints = 30

// ErrNothingToUndo is returned by Undo when no turn changed any file.
var ErrNothingToUndo = errors.New("nothing to undo")

// checkpointer records pre-change snapshots for the running turn.
type checkpointer struct {
	// turn and messages describe the running turn for the next checkpoint.
	turn, messages int
	mu             sync.Mutex
	open           bool // the last checkpoint in all belongs to the running turn
	dirty          bool // changed since the last SaveUndo
	gitTried       bool // a git snapshot was attempted this turn
	all            []Checkpoint
	// redo holds the states undone in this session, newest last; any new
	// change clears it.
	redo []Checkpoint
	// changes is the hunk-level provenance record, oldest first.
	changes []Change
	seq     int
}

// beginTurn closes the previous turn's checkpoint; the next snapshot opens
// a new one so turns without file changes leave nothing behind.
func (c *checkpointer) beginTurn(turn, messages int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open = false
	c.gitTried = false
	c.turn, c.messages = turn, messages
}

// turnStart returns the index into Agent.Messages the running turn's own
// messages begin at (the "before" beginTurn was called with), so a caller
// can bound a search to only messages made during this turn (see
// intentFor's fallback to the previous assistant message of the same turn).
func (c *checkpointer) turnStart() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messages
}

// snapshot records the state before a mutating tool runs: a git snapshot
// of the work tree on the first mutating call of the turn, and a copy of
// every path a file tool will touch, once per turn per path.
func (c *checkpointer) snapshot(ctx context.Context, repo Snapshotter, t tool.Tool, input []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if repo != nil && !c.gitTried {
		c.gitTried = true
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		snap, err := repo.Take(ctx, "before "+t.Name())
		cancel()
		if err == nil {
			if !c.open {
				c.openCheckpoint(ctx, repo)
			}
			c.all[len(c.all)-1].Snap = &snap
			c.dirty = true
		}
	}
	tc, ok := t.(tool.Toucher)
	if !ok {
		return
	}
	paths, err := tc.Touches(input)
	if err != nil {
		return // the tool itself will report the bad input
	}
	for _, p := range paths {
		if c.open && c.all[len(c.all)-1].has(p) {
			continue
		}
		snap := Snapshot{Path: p}
		if data, err := os.ReadFile(p); err == nil {
			snap.Data, snap.Existed = data, true
		} else if !errors.Is(err, os.ErrNotExist) {
			// Unreadable: the checkpoint records that it could not be
			// captured rather than quietly holding nothing, which an
			// undo would otherwise write over the file as if it were
			// the state before the turn.
			snap.Failed = true
		}
		if !c.open {
			c.openCheckpoint(ctx, repo)
		}
		cur := &c.all[len(c.all)-1]
		cur.Files = append(cur.Files, snap)
		c.dirty = true
	}
}

// openCheckpoint starts a checkpoint for the running turn. A new change
// makes the redo states unreachable, so they are dropped.
func (c *checkpointer) openCheckpoint(ctx context.Context, repo Snapshotter) {
	c.all = append(c.all, Checkpoint{When: time.Now(), Turn: c.turn, Messages: c.messages})
	c.open = true
	c.dropRedo(ctx, repo)
}

// dropRedo forgets the redo states. Dropping their refs is cleanup that
// should happen even when ctx was cancelled just before.
func (c *checkpointer) dropRedo(ctx context.Context, repo Snapshotter) {
	ctx = context.WithoutCancel(ctx)
	for _, r := range c.redo {
		if repo != nil && r.Snap != nil {
			repo.Drop(ctx, *r.Snap)
		}
	}
	c.redo = nil
}

// ErrNothingToRedo is returned by Redo when no undo can be reapplied.
var ErrNothingToRedo = errors.New("nothing to redo")

// UndoTurn is the turn the next Undo reverts, so a caller can describe
// that turn from Changes before the undo happens. ok is false when there
// is nothing to undo.
func (a *Agent) UndoTurn() (turn int, ok bool) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	if n := len(a.cp.all); n > 0 {
		return a.cp.all[n-1].Turn, true
	}
	return 0, false
}

// UndoPreview lists what Undo would do, one "verb path" per line, without
// changing anything.
func (a *Agent) UndoPreview(ctx context.Context) ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.all)
	if n == 0 {
		return nil, ErrNothingToUndo
	}
	cp := a.cp.all[n-1]
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

// Redo reapplies the most recently undone turn. It works until the next
// change or the end of the session, and returns what it restored.
func (a *Agent) Redo(ctx context.Context) ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.redo)
	if n == 0 {
		return nil, ErrNothingToRedo
	}
	entry := a.cp.redo[n-1]
	a.cp.redo = a.cp.redo[:n-1]
	// The state before redoing becomes undoable again.
	if before, ok := a.captureState(ctx, entry, "before redo"); ok {
		a.cp.all = append(a.cp.all, before)
		a.cp.open = false
		a.cp.dirty = true
	}
	done, err := a.restore(ctx, entry)
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	a.cp.mu.Lock()
	return done, err
}

// endTurn drops the turn's checkpoint when its git snapshot shows nothing
// changed (a shell command that only read), trims old checkpoints and
// persists the rest. It runs as the turn ends, cancelled or not, so its git
// calls do not inherit ctx's cancellation.
func (a *Agent) endTurn(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	a.cp.mu.Lock()
	repo := a.snaps()
	if a.cp.open && repo != nil {
		cur := &a.cp.all[len(a.cp.all)-1]
		if cur.Snap != nil && len(cur.Files) == 0 {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			changed, err := repo.Changed(cctx, *cur.Snap)
			cancel()
			if err == nil && !changed {
				repo.Drop(ctx, *cur.Snap)
				a.cp.all = a.cp.all[:len(a.cp.all)-1]
				a.cp.open = false
			}
		}
	}
	for len(a.cp.all) > maxCheckpoints {
		if repo != nil && a.cp.all[0].Snap != nil {
			repo.Drop(ctx, *a.cp.all[0].Snap)
		}
		a.cp.all = a.cp.all[1:]
		a.cp.dirty = true
	}
	dirty := a.cp.dirty
	a.cp.mu.Unlock()
	if dirty {
		if err := a.SaveUndo(); err != nil {
			a.debugf("save undo: %v", err)
		}
	}
}

func (cp *Checkpoint) has(p string) bool {
	for _, f := range cp.Files {
		if f.Path == p {
			return true
		}
	}
	return false
}

// Undo restores the files changed by the most recent turn that touched any
// and forgets that checkpoint. It returns what it restored, and an error
// listing files that could not be restored. The state before undoing is
// kept for Redo.
func (a *Agent) Undo(ctx context.Context) ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.all)
	if n == 0 {
		return nil, ErrNothingToUndo
	}
	cp := a.cp.all[n-1]
	a.cp.all = a.cp.all[:n-1]
	a.cp.open = false
	a.cp.dirty = true
	if now, ok := a.captureState(ctx, cp, "before undo"); ok {
		a.cp.redo = append(a.cp.redo, now)
	}
	done, err := a.restore(ctx, cp)
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	if err == nil {
		a.rollbackConversation(cp)
	}
	a.cp.mu.Lock()
	return done, err
}

// rollbackConversation takes an undone turn's messages out of the
// context. Reverting the files while the transcript still describes the
// edits leaves the model asserting file contents that are no longer
// there; the workspace is restored first, and only then the conversation,
// with one note in place of the turn so the model knows what happened.
// The session log keeps the turn shadowed, not deleted.
func (a *Agent) rollbackConversation(cp Checkpoint) {
	a.undoRemoved = 0
	// A checkpoint from before this field existed has Turn 0 and is left
	// alone; a first turn legitimately starts at message 0.
	if cp.Turn <= 0 || cp.Messages < 0 || cp.Messages >= len(a.Messages) {
		return
	}
	removed := len(a.Messages) - cp.Messages
	note := llm.HarnessMessage(llm.RoleUser, fmt.Sprintf("The user undid the previous turn: its file changes were reverted and its %d message(s) removed from this conversation. The files are as they were before it; read them again before relying on their contents.", removed))
	a.setMessages(append(a.Messages[:cp.Messages:cp.Messages], note))
	a.undoRemoved = removed
	if a.Log == nil {
		return
	}
	var seqs []int
	for _, e := range a.Log.Events() {
		if e.Surface && e.Turn >= cp.Turn && cp.Turn > 0 {
			seqs = append(seqs, e.Seq)
		}
	}
	a.logEvent(session.Event{Type: session.TypeUserMessage, Turn: a.turnN, Surface: true, SurfaceOp: session.SurfaceOpReplace, Replaces: seqs, Message: &note})
}

// restore puts the tree back to a checkpoint: through git when it holds a
// snapshot, else (or as a fallback) from the file copies. Called with the
// lock held.
func (a *Agent) restore(ctx context.Context, cp Checkpoint) ([]string, error) {
	var (
		done []string
		errs []error
	)
	if repo := a.snaps(); cp.Snap != nil && repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		done, err := repo.Restore(rctx, *cp.Snap)
		cancel()
		if err == nil || !errors.Is(err, snapshot.ErrHeadMoved) {
			repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
			return done, err
		}
		if len(cp.Files) == 0 {
			return nil, fmt.Errorf("%w: undo only knows how to revert this turn's shell changes through git, and HEAD is no longer where the snapshot was taken", err)
		}
		errs = append(errs, fmt.Errorf("%w; only the changes made through file tools were reverted", err))
		repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
	}
	// Restore in reverse so a file created then edited ends up removed.
	files := append([]Snapshot(nil), cp.Files...)
	sort.SliceStable(files, func(i, j int) bool { return i > j })
	for _, f := range files {
		var err error
		if f.Failed {
			errs = append(errs, fmt.Errorf("%s: its state before the turn could not be captured, so it was left as it is", f.Path))
			continue
		}
		if f.Existed {
			err = fsutil.AtomicWrite(f.Path, f.Data)
		} else if err = os.Remove(f.Path); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Path, err))
			continue
		}
		if f.Existed {
			done = append(done, "restored "+f.Path)
		} else {
			done = append(done, "removed "+f.Path)
		}
	}
	return done, errors.Join(errs...)
}
