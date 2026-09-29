package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// Snapshotter is the work-tree snapshot store the checkpointer uses: the
// consumer-side view of *snapshot.Repo, named here so the undo path can be
// exercised against a fake instead of a real git repository. The method
// set is exactly the one snapshot.Repo offers.
type Snapshotter interface {
	Take(ctx context.Context, label string) (snapshot.Snap, error)
	Restore(ctx context.Context, s snapshot.Snap) ([]string, error)
	RestoreSkip(ctx context.Context, s snapshot.Snap, skip map[string]bool) ([]string, error)
	Drop(ctx context.Context, s snapshot.Snap)
	Changed(ctx context.Context, s snapshot.Snap) (bool, error)
	Diff(ctx context.Context, s snapshot.Snap) ([]string, error)
	Moved(ctx context.Context, s snapshot.Snap) (bool, error)
}

// checkpoints is the collaborator that answers "put it back the way it
// was": the per-turn checkpoints /undo and /redo restore, the hunk-level
// provenance record behind /changes and /blame, where both are persisted,
// and the selection "/undo about" left for a following "/undo apply".
//
// Embedded in Agent by value, so UndoPath and Snapshots keep their names
// for the caller that fills them in and cp keeps its name at every call
// site inside the package.
type checkpoints struct {
	// UndoPath, when set, persists undo checkpoints next to the session so
	// /undo survives a resume.
	UndoPath string
	// Snapshots, when set, snapshots the git work tree before the first
	// mutating tool of each turn so Undo also reverts shell changes. It
	// is the concrete repository because the proof path needs its Root;
	// everything on the undo path takes it as a Snapshotter (see snaps).
	Snapshots *snapshot.Repo
	// snapshotter, when set, stands in for Snapshots everywhere on the
	// undo path. It is what the Snapshotter interface is for: a test can
	// drive undo, redo and the turn restore against a store that blocks,
	// fails, or reports a moved HEAD, with no git repository behind it.
	snapshotter Snapshotter
	// cp records pre-change snapshots for the running turn.
	cp checkpointer
	// undoRemoved is how many messages the last Undo removed.
	undoRemoved int
	// pendingUndo is what "/undo about" or "/undo turn" last showed, for
	// a following "/undo apply" (see undo_intent.go). Not persisted: it
	// does not survive a resume, the way turn-level undo checkpoints do.
	pendingUndo *pendingUndo
	// blobTemp is blobDir's directory when there is no UndoPath.
	blobTemp blobTemp
}

// snaps is Snapshots as the interface the checkpoint code consumes, and
// nil when there is no repository — a typed nil pointer wrapped in an
// interface is not nil, and every caller here tests for nil.
func (k *checkpoints) snaps() Snapshotter {
	if k.snapshotter != nil {
		return k.snapshotter
	}
	if k.Snapshots == nil {
		return nil
	}
	return k.Snapshots
}

// LastUndoRemoved is how many messages the last Undo took out of the
// conversation, for a front-end that mirrors the history.
func (k *checkpoints) LastUndoRemoved() int { return k.undoRemoved }

// Checkpoints lists the turns that changed files, oldest first.
func (k *checkpoints) Checkpoints() []Checkpoint {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	return append([]Checkpoint(nil), k.cp.all...)
}

// Changes lists the session's file changes, oldest first, with content.
func (k *checkpoints) Changes() []Change {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	return append([]Change(nil), k.cp.changes...)
}

// Change finds one change by number.
func (k *checkpoints) Change(seq int) (Change, error) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	for _, c := range k.cp.changes {
		if c.Seq == seq {
			return c, nil
		}
	}
	return Change{}, fmt.Errorf("%w: #%d", ErrNoSuchChange, seq)
}

// ChangeGroups lists the session's changes grouped by turn and intent,
// oldest first.
func (k *checkpoints) ChangeGroups() []ChangeGroup {
	var groups []ChangeGroup
	for _, c := range k.Changes() {
		if n := len(groups); n > 0 && groups[n-1].Turn == c.Turn && groups[n-1].Intent == c.Intent {
			groups[n-1].Changes = append(groups[n-1].Changes, c)
			continue
		}
		groups = append(groups, ChangeGroup{Turn: c.Turn, Intent: c.Intent, Changes: []Change{c}})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Turn < groups[j].Turn })
	return groups
}

// record appends one change to the provenance log, trimming the oldest
// once the log passes either of its bounds.
func (k *checkpoints) record(c Change) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	k.cp.seq++
	c.Seq = k.cp.seq
	c.When = time.Now()
	k.cp.changes = append(k.cp.changes, c)
	k.cp.dirty = true
	total := 0
	for _, x := range k.cp.changes {
		total += len(x.Before) + len(x.After)
	}
	for len(k.cp.changes) > maxChanges || total > maxChangesBytes && len(k.cp.changes) > 1 {
		total -= len(k.cp.changes[0].Before) + len(k.cp.changes[0].After)
		k.cp.changes = k.cp.changes[1:]
	}
}

// markUndone flags the change numbered seq as reverted.
func (k *checkpoints) markUndone(seq int) {
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	for i := range k.cp.changes {
		if k.cp.changes[i].Seq == seq {
			k.cp.changes[i].Undone = true
		}
	}
}

// captureState records the current tree as a checkpoint covering what the
// given one covers, so a restore can be reversed. It reads no checkpoint
// state of its own, and takes a git snapshot, so it is called with cp.mu
// dropped.
func (k *checkpoints) captureState(ctx context.Context, like Checkpoint, label string) (Checkpoint, bool) {
	cp := Checkpoint{When: time.Now()}
	if repo := k.snaps(); repo != nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		snap, err := repo.Take(ctx, label)
		cancel()
		if err == nil {
			cp.Snap = &snap
		}
	}
	for _, f := range like.Files {
		cp.Files = append(cp.Files, captureCopy(f.Path, k.blobDir()))
	}
	return cp, cp.Snap != nil || len(cp.Files) > 0
}

// undoFile is the on-disk shape of UndoPath. Older files hold a bare
// checkpoint array.
type undoFile struct {
	Checkpoints []Checkpoint `json:"checkpoints"`
	Redo        []Checkpoint `json:"redo,omitempty"`
	Changes     []Change     `json:"changes,omitempty"`
	Seq         int          `json:"seq,omitempty"`
}

// SaveUndo persists the checkpoints to UndoPath so /undo works after a
// resume. Without a path it does nothing.
func (k *checkpoints) SaveUndo() error {
	if k.UndoPath == "" {
		return nil
	}
	k.cp.mu.Lock()
	defer k.cp.mu.Unlock()
	k.cp.dirty = false
	if len(k.cp.all) == 0 && len(k.cp.redo) == 0 && len(k.cp.changes) == 0 {
		err := os.Remove(k.UndoPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(undoFile{Checkpoints: k.cp.all, Redo: k.cp.redo, Changes: k.cp.changes, Seq: k.cp.seq})
	if err != nil {
		return err
	}
	// A session sidecar: it holds the contents of the files the turns
	// changed, so it is private like the log beside it.
	if err := fsutil.AtomicWriteMode(k.UndoPath, data, fsutil.PrivateFileMode); err != nil {
		return err
	}
	// Only once the sidecar no longer names them: a crash between the
	// two leaves an orphan blob, never a checkpoint without its copy.
	collectBlobs(k.blobDir(), k.cp.all, k.cp.redo)
	return nil
}

// LoadUndo restores checkpoints saved by SaveUndo. A missing file is fine.
func (k *checkpoints) LoadUndo() error {
	if k.UndoPath == "" {
		return nil
	}
	data, err := os.ReadFile(k.UndoPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f undoFile
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		err = json.Unmarshal(data, &f.Checkpoints)
	} else {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", k.UndoPath, err)
	}
	k.cp.mu.Lock()
	k.cp.all, k.cp.redo, k.cp.open, k.cp.dirty = f.Checkpoints, f.Redo, false, false
	k.cp.changes, k.cp.seq = f.Changes, f.Seq
	k.cp.mu.Unlock()
	return nil
}

// LoadChanges reads the file changes a session's undo file (see UndoPath)
// records, without an agent to restore them into.
func LoadChanges(undoPath string) ([]Change, error) {
	k := &checkpoints{UndoPath: undoPath}
	if err := k.LoadUndo(); err != nil {
		return nil, err
	}
	return k.Changes(), nil
}

// reset forgets every checkpoint and change, for a session switch.
func (k *checkpoints) reset() {
	k.cp.mu.Lock()
	k.cp.all, k.cp.redo, k.cp.open, k.cp.dirty = nil, nil, false, false
	k.cp.changes, k.cp.seq = nil, 0
	k.cp.mu.Unlock()
}
