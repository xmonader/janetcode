package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
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
	// Blob, when set, is the file holding the copy instead of Data: a
	// file over inlineCopyBytes is streamed there rather than held in
	// memory and in the undo sidecar (see cpcopy.go).
	Blob string `json:"blob,omitempty"`
	// TooLarge marks a file over maxCopyBytes: its state before the turn
	// was not copied, and an undo leaves it alone and says so.
	TooLarge bool `json:"too_large,omitempty"`
	// Size is the file's size when it was captured as a blob or found too
	// large, for the messages that talk about it.
	Size int64 `json:"size,omitempty"`
}

// Checkpoint is the state before a turn that changed something. Inside a
// git repository Snap holds the whole work tree (so shell changes revert
// too); Files are the copies the file tools made, used outside git or when
// the snapshot cannot be restored.
type Checkpoint struct {
	When  time.Time      `json:"when"`
	Files []Snapshot     `json:"files"`
	Snap  *snapshot.Snap `json:"snap,omitempty"`
	// Touched is the manifest of every path the turn left different from
	// the snapshot — path to the sha256 of its content at end of turn
	// ("" for a path the turn deleted, "!" for one that could not be
	// hashed). Undo refuses to touch a path whose current content no
	// longer matches this manifest: that change came from somewhere else
	// — the user's own hand, usually — and must survive the restore.
	Touched []TouchedFile `json:"touched,omitempty"`
	// Manifested says Touched was recorded for this checkpoint, which is
	// not the same as Touched being non-empty. A turn that left the tree
	// exactly as it found it has an empty manifest and an undo must touch
	// nothing; a checkpoint saved before manifests existed has no
	// manifest and restores everything, as it always did. Reading the
	// first as the second is what made an undo clobber work done after
	// the turn.
	Manifested bool `json:"manifested,omitempty"`
	// Turn is the turn the checkpoint belongs to and Messages how many
	// messages the conversation held when it began, so an undo can take
	// the turn's messages out of the context along with its file changes.
	Turn     int `json:"turn,omitempty"`
	Messages int `json:"messages,omitempty"`
}

// TouchedFile is one path a turn changed, with the hash of what the turn
// left there so a later undo can tell the turn's work apart from edits
// made after it.
type TouchedFile struct {
	Path string `json:"path"`
	Sum  string `json:"sum"`
}

// maxManifestBytes bounds what one manifest entry will hash. The hash is
// streamed, so a huge file costs time rather than memory, and a turn that
// wrote one is rare — above this the entry records "!" and the undo
// leaves the path alone and says why, which is the safe direction.
const maxManifestBytes = 256 << 20

// hashPath is the manifest hash of a path's current state: the sha256 of
// a regular file's content, of "symlink:"+target for a symlink, and "!"
// for anything else — a device, a socket, a path that cannot be read.
//
// Lstat, never Stat: following a link would hash the target's content, so
// a turn that replaced a file with a symlink (git reports that as T) and
// a tree where it never happened would hash the same.
func hashPath(path string) string {
	st, err := os.Lstat(path)
	switch {
	case err != nil:
		return "!"
	case st.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "!"
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte("symlink:"+target)))
	case !st.Mode().IsRegular() || st.Size() > maxManifestBytes:
		return "!"
	}
	f, err := os.Open(path)
	if err != nil {
		return "!"
	}
	defer f.Close()
	h := sha256.New()
	// Streamed: a turn that wrote an 8 GB file used to be hashed with
	// os.ReadFile, which held all of it in memory before the turn could
	// return.
	if _, err := io.Copy(h, f); err != nil {
		return "!"
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// withFileCopies adds the end-of-turn state of every path the file tools
// touched to a manifest, for the paths git did not already account for.
// Snapshot.Diff builds its list with `git add -A`, which honours
// .gitignore — so a turn that wrote an ignored file would be missing from
// the manifest and its own work would read as somebody else's. Outside a
// git repository this is the whole manifest: the file copies are then the
// only record of what the turn did.
func withFileCopies(touched []TouchedFile, files []Snapshot) []TouchedFile {
	have := make(map[string]bool, len(touched))
	for _, tf := range touched {
		have[tf.Path] = true
	}
	for _, f := range files {
		if have[f.Path] {
			continue
		}
		have[f.Path] = true
		// An empty sum is "the turn left nothing here", which is what a
		// path the turn deleted must record.
		sum := ""
		if _, err := os.Lstat(f.Path); err == nil {
			sum = hashPath(f.Path)
		}
		touched = append(touched, TouchedFile{Path: f.Path, Sum: sum})
	}
	return touched
}

// sameSnap reports whether two snapshot pointers name the same state,
// counting "neither has one" as the same — a checkpoint outside git.
func sameSnap(a, b *snapshot.Snap) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Commit == b.Commit
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
// every path a file tool will touch, once per turn per path. It returns
// the copies it found too large to take (see maxCopyBytes).
func (c *checkpointer) snapshot(ctx context.Context, repo Snapshotter, blobs func() string, t tool.Tool, input []byte) (tooLarge []Snapshot) {
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
		return nil
	}
	paths, err := tc.Touches(input)
	if err != nil {
		return nil // the tool itself will report the bad input
	}
	for _, p := range paths {
		if c.open && c.all[len(c.all)-1].has(p) {
			continue
		}
		snap := captureCopy(p, blobs)
		if snap.TooLarge {
			tooLarge = append(tooLarge, snap)
		}
		if !c.open {
			c.openCheckpoint(ctx, repo)
		}
		cur := &c.all[len(c.all)-1]
		cur.Files = append(cur.Files, snap)
		c.dirty = true
	}
	return tooLarge
}

// snapshotBefore records the state before a mutating call on whichever
// agent owns the undo stack for this workspace — the parent, when this
// agent is a fork sharing it. A file too large for the checkpoint to copy
// is said as it happens, outside git (whose snapshot still holds it): the
// user learns that /undo cannot put it back before the call changes it,
// not after.
func (a *Agent) snapshotBefore(ctx context.Context, t tool.Tool, input []byte, emit func(Event)) {
	// The turn's first change waits for the pre-edit check run, so the
	// baseline saw the tree this call is about to change.
	a.awaitBaseline(ctx, emit)
	owner := a
	if a.UndoOwner != nil {
		owner = a.UndoOwner
	}
	repo := owner.snaps()
	for _, f := range owner.cp.snapshot(ctx, repo, owner.blobDir, t, input) {
		if repo == nil && emit != nil {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf("checkpoint: %s is %d MiB, over the %d MiB a checkpoint copies — /undo will not be able to put it back", f.Path, f.Size>>20, maxCopyBytes>>20)})
		}
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
	if a.running.Load() {
		return nil, ErrTurnRunning
	}
	entry, ok := a.popRedo()
	if !ok {
		return nil, ErrNothingToRedo
	}
	// The state before redoing becomes undoable again.
	before, captured := a.captureState(ctx, entry, "before redo")
	if captured {
		a.pushCheckpoint(before)
	}
	done, err := a.restore(ctx, entry, true)
	if errors.As(err, new(nothingReverted)) {
		a.unpop(ctx, entry, before, captured, &a.cp.redo, &a.cp.all)
		return nil, err
	}
	if captured {
		a.manifestCaptured(ctx, before, &a.cp.all)
	}
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	return done, err
}

// manifestCaptured records what the restore just performed changed as the
// manifest of the state captured before it, in place at the top of the
// stack it was pushed onto.
//
// That captured state is a checkpoint like any other — a redo can restore
// it — and captureState cannot manifest it, because what it covers is
// exactly what the restore that follows changes. Left unmanifested, one
// redo made the next undo restore the whole tree again.
func (a *Agent) manifestCaptured(ctx context.Context, cp Checkpoint, stack *[]Checkpoint) {
	var touched []TouchedFile
	if repo := a.snaps(); repo != nil && cp.Snap != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		t, err := a.touchedManifest(cctx, *cp.Snap)
		cancel()
		if err != nil {
			return // no manifest restores everything, as it always did
		}
		touched = t
	}
	touched = withFileCopies(touched, cp.Files)
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	if n := len(*stack); n > 0 && !(*stack)[n-1].Manifested && sameSnap((*stack)[n-1].Snap, cp.Snap) {
		(*stack)[n-1].Touched, (*stack)[n-1].Manifested = touched, true
		a.cp.dirty = true
	}
}

// endTurn drops the turn's checkpoint when its git snapshot shows nothing
// changed (a shell command that only read), trims old checkpoints and
// persists the rest. It runs as the turn ends, cancelled or not, so its git
// calls do not inherit ctx's cancellation.
func (a *Agent) endTurn(ctx context.Context) {
	// The turn is not over until its manifest is recorded. Clearing this
	// first left a window — up to a minute of git — in which Undo was
	// accepted against a checkpoint that had no manifest yet, and so
	// restored the whole tree.
	defer a.running.Store(false)
	defer a.endAskTurn()
	// The turn's event sink goes with the turn: emitter() promises a sink
	// that drops events when no turn is running, and without this it kept
	// handing out the finished turn's one.
	emit := a.emitter() // the turn's sink, to say which checkpoint it left
	a.setEmitter(nil)
	ctx = context.WithoutCancel(ctx)
	a.cp.mu.Lock()
	repo := a.snaps()
	var (
		snap     *snapshot.Snap // the open checkpoint's snapshot, to manifest
		files    []Snapshot     // and the copies its file tools made
		want     bool           // it still needs a manifest
		announce int            // the turn whose checkpoint survived, to name
	)
	if a.cp.open {
		cur := &a.cp.all[len(a.cp.all)-1]
		if repo != nil && cur.Snap != nil && len(cur.Files) == 0 {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			changed, err := repo.Changed(cctx, *cur.Snap)
			cancel()
			if err == nil && !changed {
				repo.Drop(ctx, *cur.Snap)
				a.cp.all = a.cp.all[:len(a.cp.all)-1]
				a.cp.open = false
			}
		}
		if a.cp.open {
			announce = a.cp.all[len(a.cp.all)-1].Turn
			if cur := &a.cp.all[len(a.cp.all)-1]; !cur.Manifested {
				want, files = true, cur.Files
				if repo != nil {
					snap = cur.Snap
				}
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
	if want {
		// Record what the turn actually left in the tree, so a later
		// undo can tell the turn's work apart from edits made after it.
		// This is git work plus file reads, so it runs with the lock
		// dropped; the result lands only if the checkpoint is still the
		// open one.
		var (
			touched []TouchedFile
			err     error
		)
		if snap != nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			touched, err = a.touchedManifest(cctx, *snap)
			cancel()
		}
		// A manifest git could not produce is left unrecorded: no
		// manifest restores everything, which is what this checkpoint
		// did before manifests existed. Recording a partial one would
		// silently narrow the undo instead.
		if err == nil {
			touched = withFileCopies(touched, files)
			a.cp.mu.Lock()
			if a.cp.open {
				if cur := &a.cp.all[len(a.cp.all)-1]; !cur.Manifested && sameSnap(cur.Snap, snap) {
					cur.Touched, cur.Manifested = touched, true
					a.cp.dirty = true
					dirty = true
				}
			}
			a.cp.mu.Unlock()
		}
	}
	if dirty {
		if err := a.SaveUndo(); err != nil {
			a.debugf("save undo: %v", err)
		}
	}
	if announce > 0 {
		a.announceCheckpoint(emit, announce)
	}
}

// announceCheckpoint says, as the turn ends, which checkpoint it left: a
// turn that changed something is recoverable, and the person should see
// the identifier without having to ask for the list.
func (a *Agent) announceCheckpoint(emit func(Event), turn int) {
	for _, ci := range a.CheckpointList() {
		if ci.Turn != turn {
			continue
		}
		text := "checkpoint " + ci.ID
		if ci.Changed >= 0 {
			text += " · " + plural(ci.Changed, "path") + " changed"
		}
		if !ci.Complete {
			text += " · incomplete"
		}
		emit(Event{Kind: EvNotice, Text: text + " · /checkpoints compares, exports or restores it"})
		return
	}
}

// touchedManifest hashes every path the tree holds differently from the
// snapshot: the state the turn left behind, recorded so undo can refuse
// to clobber later edits.
func (a *Agent) touchedManifest(ctx context.Context, snap snapshot.Snap) ([]TouchedFile, error) {
	repo := a.snaps()
	if repo == nil {
		return nil, errors.New("no repository")
	}
	lines, err := repo.Diff(ctx, snap)
	if err != nil {
		return nil, err
	}
	var out []TouchedFile
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		tf := TouchedFile{Path: path}
		switch line[0] {
		case 'D':
			// The turn deleted it; the manifest records absence.
		case 'M', 'A', 'T':
			// T is a typechange — a file the turn replaced with a
			// symlink, or the reverse. Dropping it left the turn's own
			// work outside the manifest, so the undo skipped it and told
			// the user it had changed after the turn.
			tf.Sum = hashPath(path)
		default:
			// Some other status: record it as unhashable rather than
			// leaving it out. Out of the manifest reads as "somebody
			// else changed this", which is a lie about a path the turn
			// touched; "!" says the truth and still leaves it alone.
			tf.Sum = "!"
		}
		out = append(out, tf)
	}
	return out, nil
}

func (cp *Checkpoint) has(p string) bool {
	for _, f := range cp.Files {
		if f.Path == p {
			return true
		}
	}
	return false
}

// isForeign reports whether the current state of path is something other
// than what the turn left there — so a restore must leave it alone — and
// why, for the user. It is the whole manifest check, shared by the git
// restore and the file-copy fallback so the two cannot disagree.
func isForeign(manifest map[string]string, path string) (string, bool) {
	want, ok := manifest[path]
	if !ok {
		return "it changed after the turn ended, not during it", true
	}
	if want == "!" {
		return "its state at the end of the turn could not be recorded", true
	}
	_, statErr := os.Lstat(path)
	if want == "" { // the turn left it deleted
		if statErr == nil {
			return "the turn deleted it, but it exists again — something recreated it afterwards", true
		}
		return "", false
	}
	if statErr != nil {
		return "it is gone now", true
	}
	if hashPath(path) != want {
		return "it was modified after the turn, not by it", true
	}
	return "", false
}

// foreignChanges compares the tree against the checkpoint's Touched
// manifest and reports what no longer matches: paths the restore would
// otherwise clobber whose current state is not the turn's work. The
// returned set is what the git restore must skip, and the reasons say
// why, one per skipped path.
func (a *Agent) foreignChanges(ctx context.Context, repo Snapshotter, cp Checkpoint) (map[string]bool, map[string]string, error) {
	skip := map[string]bool{}
	reasons := map[string]string{}
	manifest := make(map[string]string, len(cp.Touched))
	for _, tf := range cp.Touched {
		manifest[tf.Path] = tf.Sum
	}
	lines, err := repo.Diff(ctx, *cp.Snap)
	if err != nil {
		// Without this list there is no way to tell the turn's work from
		// the user's, and an empty skip set means "restore everything".
		// Returning it as an error refuses the undo instead.
		return nil, nil, err
	}
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		if why, foreign := isForeign(manifest, path); foreign {
			skip[path] = true
			reasons[path] = why
		}
	}
	return skip, reasons, nil
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
	if a.running.Load() {
		return nil, ErrTurnRunning
	}
	cp, ok := a.popCheckpoint()
	if !ok {
		return nil, ErrNothingToUndo
	}
	now, captured := a.captureState(ctx, cp, "before undo")
	if captured {
		a.pushRedo(now)
	}
	done, err := a.restore(ctx, cp, true)
	if errors.As(err, new(nothingReverted)) {
		// Nothing changed, so the turn is still there to undo: popped
		// for good, a retry once git recovered undid the turn before it
		// instead, and a redo "restored" the state the tree was already in.
		a.unpop(ctx, cp, now, captured, &a.cp.all, &a.cp.redo)
		return nil, err
	}
	if captured {
		a.manifestCaptured(ctx, now, &a.cp.redo)
	}
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	if err == nil {
		a.rollbackConversation(cp)
	}
	return done, err
}

// nothingReverted is a restore that failed before it touched anything.
type nothingReverted struct{ error }

func (e nothingReverted) Unwrap() error { return e.error }

// unpop reverses an Undo or Redo that reverted nothing: the entry goes
// back on the stack it was popped from (to, all or redo), and the state
// captured before it comes off the other one (from). Its git ref is
// dropped with the lock released, as every git call here is.
func (a *Agent) unpop(ctx context.Context, entry, captured Checkpoint, pushed bool, to, from *[]Checkpoint) {
	a.cp.mu.Lock()
	*to = append(*to, entry)
	a.cp.dirty = true
	n := len(*from)
	drop := pushed && n > 0 && (*from)[n-1].When.Equal(captured.When) && sameSnap((*from)[n-1].Snap, captured.Snap)
	if drop {
		*from = (*from)[:n-1]
	}
	a.cp.mu.Unlock()
	if repo := a.snaps(); drop && repo != nil && captured.Snap != nil {
		repo.Drop(context.WithoutCancel(ctx), *captured.Snap)
	}
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
	// The recorded count is an index into a history a compaction may have
	// rewritten since; cutting there without checking dropped whatever sat
	// past it, and shadowing the turn's events by number took the
	// compaction's summary with them. rewindCut checks it against the log,
	// as /undo turn does; when the turn's start cannot be found the
	// conversation is left whole and the model is told what happened.
	keep, ok, _ := a.rewindCut(cp)
	if !ok {
		a.appendHarnessText("[harness] The user undid a previous turn: its file changes were reverted, and the files are as they were before it. Read them again before relying on what an earlier message said about them.")
		return
	}
	removed := len(a.Messages) - keep
	note := llm.HarnessMessage(llm.RoleUser, fmt.Sprintf("The user undid the previous turn: its file changes were reverted and its %d message(s) removed from this conversation. The files are as they were before it; read them again before relying on their contents.", removed))
	if !a.rewindThroughLog(keep, note) {
		a.setMessages(append(a.Messages[:keep:keep], note))
	}
	a.undoRemoved = removed
}

// restore puts the tree back to a checkpoint: through git when it holds a
// snapshot, else (or as a fallback) from the file copies. cp is a copy, so
// it takes no lock — and it must not be called with cp.mu held, since it
// can spend a minute in git.
//
// scoped is what /undo means: touch only what the checkpoint can account
// for. /undo turn N means the other thing — put the whole tree back to
// before that turn, whatever has happened since — and passes false, as
// its git path has always done by calling Restore directly.
//
// When scoped, a restore only touches what the checkpoint can account
// for, by either route. The turn's Touched manifest records the content hash of every
// path it left changed; a path whose current content no longer matches —
// edited by the user after the turn, or created by something else
// entirely — is skipped and reported, not clobbered. Only a checkpoint
// saved before manifests existed (Manifested false) restores everything,
// as it always did.
func (a *Agent) restore(ctx context.Context, cp Checkpoint, scoped bool) ([]string, error) {
	var (
		done []string
		errs []error
	)
	if repo := a.snaps(); cp.Snap != nil && repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		// reverted, not done: what git put back is returned as it is on
		// the paths below, and on the one path that falls through, git
		// put nothing back at all.
		var (
			reverted []string
			err      error
		)
		if scoped && cp.Manifested {
			skip, reasons, ferr := a.foreignChanges(rctx, repo, cp)
			if ferr != nil {
				cancel()
				return nil, nothingReverted{fmt.Errorf("undo needs to know which files this turn changed so it does not revert anything else, and git could not say (%w): nothing was reverted", ferr)}
			}
			reverted, err = repo.RestoreSkip(rctx, *cp.Snap, skip)
			keys := make([]string, 0, len(reasons))
			for k := range reasons {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, path := range keys {
				done = append(done, "left "+path+" alone: "+reasons[path])
			}
		} else {
			reverted, err = repo.Restore(rctx, *cp.Snap)
		}
		cancel()
		if err == nil || !errors.Is(err, snapshot.ErrHeadMoved) {
			// What git does not record — an ignored file, one outside the
			// work tree — only the file tools' copies can put back.
			// Returning here without them left a turn's edit to .env in
			// place and reported the undo done.
			rest, uerr := uncoveredCopies(context.WithoutCancel(ctx), repo, *cp.Snap, cp.Files)
			repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
			d, cerrs := restoreCopies(rest, cp, scoped)
			return append(append(done, reverted...), d...), errors.Join(append([]error{err, uerr}, cerrs...)...)
		}
		if len(cp.Files) == 0 {
			return nil, fmt.Errorf("%w: undo only knows how to revert this turn's shell changes through git, and HEAD is no longer where the snapshot was taken", err)
		}
		errs = append(errs, fmt.Errorf("%w; only the changes made through file tools were reverted", err))
		repo.Drop(context.WithoutCancel(ctx), *cp.Snap)
	}
	d, cerrs := restoreCopies(cp.Files, cp, scoped)
	return append(done, d...), errors.Join(append(errs, cerrs...)...)
}

// uncoveredCopies are the file copies of the paths a git restore of snap
// leaves untouched (see snapshot.Repo.Uncovered). A Snapshotter that
// cannot say — a test's fake — covers everything, as a git restore was
// always taken to.
func uncoveredCopies(ctx context.Context, repo Snapshotter, snap snapshot.Snap, files []Snapshot) ([]Snapshot, error) {
	u, ok := repo.(interface {
		Uncovered(context.Context, snapshot.Snap, []string) ([]string, error)
	})
	if !ok || len(files) == 0 {
		return nil, nil
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := u.Uncovered(ctx, snap, paths)
	if err != nil {
		return nil, fmt.Errorf("git could not say which of the edited files it tracks, so any it ignores or that lie outside the repository were not reverted: %w", err)
	}
	want := make(map[string]bool, len(out))
	for _, p := range out {
		want[p] = true
	}
	var rest []Snapshot
	for _, f := range files {
		if want[f.Path] {
			rest = append(rest, f)
		}
	}
	return rest, nil
}

// restoreCopies puts back the file tools' copies of the state before the
// turn: the fallback when git cannot restore, and the whole story for a
// path git does not record. scoped is restore's.
func restoreCopies(files []Snapshot, cp Checkpoint, scoped bool) ([]string, []error) {
	var (
		done []string
		errs []error
	)
	// Restore in reverse so a file created then edited ends up removed.
	// (This was a sort.SliceStable whose "less" compared the indices it
	// was handed rather than the elements at them, which is not an
	// ordering at all and only reversed the slice by accident of the
	// algorithm in use.)
	files = slices.Clone(files)
	slices.Reverse(files)
	// The same manifest guard the git path gets. Without it this branch —
	// every workspace that is not a git repository, and a git one whose
	// HEAD moved — wrote the pre-turn bytes back over whatever was there.
	manifest := make(map[string]string, len(cp.Touched))
	for _, tf := range cp.Touched {
		manifest[tf.Path] = tf.Sum
	}
	for _, f := range files {
		var err error
		if scoped && cp.Manifested {
			if why, foreign := isForeign(manifest, f.Path); foreign {
				done = append(done, "left "+f.Path+" alone: "+why)
				continue
			}
		}
		if f.Failed {
			errs = append(errs, fmt.Errorf("%s: its state before the turn could not be captured, so it was left as it is", f.Path))
			continue
		}
		if f.TooLarge {
			errs = append(errs, fmt.Errorf("%s: %s", f.Path, tooLargeReason(f)))
			continue
		}
		if f.Existed {
			err = restoreCopy(f)
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
	return done, errs
}
