package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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
	n := len(a.cp.all)
	if n == 0 {
		a.cp.mu.Unlock()
		return nil, ErrNothingToUndo
	}
	cp := a.cp.all[n-1]
	a.cp.mu.Unlock()
	// The checkpoint is a copy, so the git diff behind the preview runs
	// with the lock dropped.
	return a.checkpointPreview(ctx, cp)
}

// Redo reapplies the most recently undone turn. It works until the next
// change or the end of the session, and returns what it restored.
//
// Like Undo it is a three-phase state machine — pop under the lock, do
// the git and file work with the lock dropped, persist afterwards — so no
// git call is ever made with cp.mu held.
func (a *Agent) Redo(ctx context.Context) ([]string, error) {
	entry, ok := a.popRedo()
	if !ok {
		return nil, ErrNothingToRedo
	}
	// The state before redoing becomes undoable again.
	if before, ok := a.captureState(ctx, entry, "before redo"); ok {
		a.pushCheckpoint(before)
	}
	done, err := a.restore(ctx, entry)
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	return done, err
}

// endTurn drops the turn's checkpoint when its git snapshot shows nothing
// changed (a shell command that only read), trims old checkpoints and
// persists the rest. It runs as the turn ends, cancelled or not, so its git
// calls do not inherit ctx's cancellation.
func (a *Agent) endTurn(ctx context.Context) {
	a.running.Store(false)
	// The turn's event sink goes with the turn: emitter() promises a sink
	// that drops events when no turn is running, and without this it kept
	// handing out the finished turn's one.
	a.setEmitter(nil)
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
// It is a state machine whose lock is held only in its bookkeeping
// phases: pop the newest checkpoint (locked), snapshot the tree it is
// about to replace and put the files back (unlocked, up to a minute of
// git), push that snapshot onto the redo stack (locked), then persist and
// rewind the conversation (unlocked). Nothing that can block runs under
// cp.mu, so a front-end reading Checkpoints or Changes while an undo runs
// is never held up behind git.
func (a *Agent) Undo(ctx context.Context) ([]string, error) {
	cp, ok := a.popCheckpoint()
	if !ok {
		return nil, ErrNothingToUndo
	}
	if now, ok := a.captureState(ctx, cp, "before undo"); ok {
		a.pushRedo(now)
	}
	done, err := a.restore(ctx, cp)
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	if err == nil {
		a.rollbackConversation(cp)
	}
	return done, err
}

// popCheckpoint takes the newest checkpoint off the undo stack. ok is
// false when there is nothing to undo. It is the only place an undo
// touches the stack before its git work, so the work itself needs no
// lock.
func (k *checkpoints) popCheckpoint() (Checkpoint, bool) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	n := len(k.cp.all)
	if n == 0 {
		return Checkpoint{}, false
	}
	cp := k.cp.all[n-1]
	k.cp.all = k.cp.all[:n-1]
	k.cp.open = false
	k.cp.dirty = true
	return cp, true
}

// popCheckpointForTurn is popCheckpoint restricted to a checkpoint that
// belongs to turn (or predates turn numbering, Turn 0): anything else
// means this turn left nothing of its own to put back.
func (k *checkpoints) popCheckpointForTurn(turn int) (Checkpoint, error) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	n := len(k.cp.all)
	if n == 0 {
		return Checkpoint{}, ErrNothingToUndo
	}
	if cp := k.cp.all[n-1]; cp.Turn == turn || cp.Turn == 0 {
		k.cp.all = k.cp.all[:n-1]
		k.cp.open = false
		k.cp.dirty = true
		return cp, nil
	}
	return Checkpoint{}, fmt.Errorf("no checkpoint for turn %d", turn)
}

// pushCheckpoint puts a state on the undo stack, closing whatever
// checkpoint the running turn had open: the state a redo replaced, or a
// restore's own "before".
func (k *checkpoints) pushCheckpoint(cp Checkpoint) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	k.cp.all = append(k.cp.all, cp)
	k.cp.open = false
	k.cp.dirty = true
}

// popRedo takes the newest state off the redo stack. ok is false when
// there is nothing to redo.
func (k *checkpoints) popRedo() (Checkpoint, bool) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	n := len(k.cp.redo)
	if n == 0 {
		return Checkpoint{}, false
	}
	cp := k.cp.redo[n-1]
	k.cp.redo = k.cp.redo[:n-1]
	k.cp.dirty = true
	return cp, true
}

// pushRedo makes a state reachable again through Redo.
func (k *checkpoints) pushRedo(cp Checkpoint) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	k.cp.redo = append(k.cp.redo, cp)
	k.cp.dirty = true
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
// snapshot, else (or as a fallback) from the file copies. cp is a copy, so
// it takes no lock — and it must not be called with cp.mu held, since it
// can spend a minute in git.
func (a *Agent) restore(ctx context.Context, cp Checkpoint) ([]string, error) {
	var (
		done []string
		errs []error
	)
	if repo := a.snaps(); cp.Snap != nil && repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		// reverted, not done: what git put back is returned as it is on
		// the paths below, and on the one path that falls through, git
		// put nothing back at all.
		reverted, err := repo.Restore(rctx, *cp.Snap)
		cancel()
		if err == nil || !errors.Is(err, snapshot.ErrHeadMoved) {
			repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
			return reverted, err
		}
		if len(cp.Files) == 0 {
			return nil, fmt.Errorf("%w: undo only knows how to revert this turn's shell changes through git, and HEAD is no longer where the snapshot was taken", err)
		}
		errs = append(errs, fmt.Errorf("%w; only the changes made through file tools were reverted", err))
		repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
	}
	// Restore in reverse so a file created then edited ends up removed.
	// (This was a sort.SliceStable whose "less" compared the indices it
	// was handed rather than the elements at them, which is not an
	// ordering at all and only reversed the slice by accident of the
	// algorithm in use.)
	files := append([]Snapshot(nil), cp.Files...)
	slices.Reverse(files)
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
