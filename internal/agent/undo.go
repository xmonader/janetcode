package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// Snapshot is a file as it was before a turn changed it.
type Snapshot struct {
	Path    string `json:"path"`
	Data    []byte `json:"data,omitempty"`
	Existed bool   `json:"existed"`
}

// Checkpoint is the state before a turn that changed something. Inside a
// git repository Snap holds the whole work tree (so shell changes revert
// too); Files are the copies the file tools made, used outside git or when
// the snapshot cannot be restored.
type Checkpoint struct {
	When  time.Time      `json:"when"`
	Files []Snapshot     `json:"files"`
	Snap  *snapshot.Snap `json:"snap,omitempty"`
}

// maxCheckpoints bounds how many turns can be undone; older ones are
// forgotten and their git refs dropped.
const maxCheckpoints = 30

// ErrNothingToUndo is returned by Undo when no turn changed any file.
var ErrNothingToUndo = errors.New("nothing to undo")

// checkpointer records pre-change snapshots for the running turn.
type checkpointer struct {
	mu       sync.Mutex
	open     bool // the last checkpoint in all belongs to the running turn
	dirty    bool // changed since the last SaveUndo
	gitTried bool // a git snapshot was attempted this turn
	all      []Checkpoint
	// redo holds the states undone in this session, newest last; any new
	// change clears it.
	redo []Checkpoint
}

// beginTurn closes the previous turn's checkpoint; the next snapshot opens
// a new one so turns without file changes leave nothing behind.
func (c *checkpointer) beginTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open = false
	c.gitTried = false
}

// snapshot records the state before a mutating tool runs: a git snapshot
// of the work tree on the first mutating call of the turn, and a copy of
// every path a file tool will touch, once per turn per path.
func (c *checkpointer) snapshot(repo *snapshot.Repo, t tool.Tool, input []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if repo != nil && !c.gitTried {
		c.gitTried = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		snap, err := repo.Take(ctx, "before "+t.Name())
		cancel()
		if err == nil {
			if !c.open {
				c.openCheckpoint(repo)
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
			continue // unreadable: cannot promise a restore
		}
		if !c.open {
			c.openCheckpoint(repo)
		}
		cur := &c.all[len(c.all)-1]
		cur.Files = append(cur.Files, snap)
		c.dirty = true
	}
}

// openCheckpoint starts a checkpoint for the running turn. A new change
// makes the redo states unreachable, so they are dropped.
func (c *checkpointer) openCheckpoint(repo *snapshot.Repo) {
	c.all = append(c.all, Checkpoint{When: time.Now()})
	c.open = true
	c.dropRedo(repo)
}

func (c *checkpointer) dropRedo(repo *snapshot.Repo) {
	for _, r := range c.redo {
		if repo != nil && r.Snap != nil {
			repo.Drop(context.Background(), *r.Snap)
		}
	}
	c.redo = nil
}

// ErrNothingToRedo is returned by Redo when no undo can be reapplied.
var ErrNothingToRedo = errors.New("nothing to redo")

// UndoPreview lists what Undo would do, one "verb path" per line, without
// changing anything.
func (a *Agent) UndoPreview() ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.all)
	if n == 0 {
		return nil, ErrNothingToUndo
	}
	cp := a.cp.all[n-1]
	var out []string
	seen := map[string]bool{}
	if cp.Snap != nil && a.Snapshots != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		diff, err := a.Snapshots.Diff(ctx, *cp.Snap)
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
func (a *Agent) Redo() ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.redo)
	if n == 0 {
		return nil, ErrNothingToRedo
	}
	entry := a.cp.redo[n-1]
	a.cp.redo = a.cp.redo[:n-1]
	// The state before redoing becomes undoable again.
	if before, ok := a.captureState(entry, "before redo"); ok {
		a.cp.all = append(a.cp.all, before)
		a.cp.open = false
		a.cp.dirty = true
	}
	done, err := a.restore(entry)
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	a.cp.mu.Lock()
	return done, err
}

// captureState records the current tree as a checkpoint covering what
// the given one covers, so the restore can be reversed. Called with the
// lock held.
func (a *Agent) captureState(like Checkpoint, label string) (Checkpoint, bool) {
	cp := Checkpoint{When: time.Now()}
	if a.Snapshots != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		snap, err := a.Snapshots.Take(ctx, label)
		cancel()
		if err == nil {
			cp.Snap = &snap
		}
	}
	for _, f := range like.Files {
		s := Snapshot{Path: f.Path}
		if data, err := os.ReadFile(f.Path); err == nil {
			s.Data, s.Existed = data, true
		}
		cp.Files = append(cp.Files, s)
	}
	return cp, cp.Snap != nil || len(cp.Files) > 0
}

// SaveUndo persists the checkpoints to UndoPath so /undo works after a
// resume. Without a path it does nothing.
func (a *Agent) SaveUndo() error {
	if a.UndoPath == "" {
		return nil
	}
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	a.cp.dirty = false
	if len(a.cp.all) == 0 && len(a.cp.redo) == 0 {
		err := os.Remove(a.UndoPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(undoFile{Checkpoints: a.cp.all, Redo: a.cp.redo})
	if err != nil {
		return err
	}
	return tool.AtomicWrite(a.UndoPath, data)
}

// undoFile is the on-disk shape of UndoPath. Older files hold a bare
// checkpoint array.
type undoFile struct {
	Checkpoints []Checkpoint `json:"checkpoints"`
	Redo        []Checkpoint `json:"redo,omitempty"`
}

// LoadUndo restores checkpoints saved by SaveUndo. A missing file is fine.
func (a *Agent) LoadUndo() error {
	if a.UndoPath == "" {
		return nil
	}
	data, err := os.ReadFile(a.UndoPath)
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
		return fmt.Errorf("%s: %w", a.UndoPath, err)
	}
	a.cp.mu.Lock()
	a.cp.all, a.cp.redo, a.cp.open, a.cp.dirty = f.Checkpoints, f.Redo, false, false
	a.cp.mu.Unlock()
	return nil
}

// endTurn drops the turn's checkpoint when its git snapshot shows nothing
// changed (a shell command that only read), trims old checkpoints and
// persists the rest.
func (a *Agent) endTurn() {
	a.cp.mu.Lock()
	if a.cp.open && a.Snapshots != nil {
		cur := &a.cp.all[len(a.cp.all)-1]
		if cur.Snap != nil && len(cur.Files) == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			changed, err := a.Snapshots.Changed(ctx, *cur.Snap)
			cancel()
			if err == nil && !changed {
				a.Snapshots.Drop(context.Background(), *cur.Snap)
				a.cp.all = a.cp.all[:len(a.cp.all)-1]
				a.cp.open = false
			}
		}
	}
	for len(a.cp.all) > maxCheckpoints {
		if a.Snapshots != nil && a.cp.all[0].Snap != nil {
			a.Snapshots.Drop(context.Background(), *a.cp.all[0].Snap)
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

// Checkpoints lists the turns that changed files, oldest first.
func (a *Agent) Checkpoints() []Checkpoint {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	return append([]Checkpoint(nil), a.cp.all...)
}

// Undo restores the files changed by the most recent turn that touched any
// and forgets that checkpoint. It returns what it restored, and an error
// listing files that could not be restored. The state before undoing is
// kept for Redo.
func (a *Agent) Undo() ([]string, error) {
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
	if now, ok := a.captureState(cp, "before undo"); ok {
		a.cp.redo = append(a.cp.redo, now)
	}
	done, err := a.restore(cp)
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	a.cp.mu.Lock()
	return done, err
}

// restore puts the tree back to a checkpoint: through git when it holds a
// snapshot, else (or as a fallback) from the file copies. Called with the
// lock held.
func (a *Agent) restore(cp Checkpoint) ([]string, error) {
	var (
		done []string
		errs []error
	)
	if cp.Snap != nil && a.Snapshots != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		done, err := a.Snapshots.Restore(ctx, *cp.Snap)
		cancel()
		if err == nil || !errors.Is(err, snapshot.ErrHeadMoved) {
			a.Snapshots.Drop(context.Background(), *cp.Snap)
			return done, err
		}
		if len(cp.Files) == 0 {
			return nil, fmt.Errorf("%w: undo only knows how to revert this turn's shell changes through git, and HEAD is no longer where the snapshot was taken", err)
		}
		errs = append(errs, fmt.Errorf("%w; only the changes made through file tools were reverted", err))
		a.Snapshots.Drop(context.Background(), *cp.Snap)
	}
	// Restore in reverse so a file created then edited ends up removed.
	files := append([]Snapshot(nil), cp.Files...)
	sort.SliceStable(files, func(i, j int) bool { return i > j })
	for _, f := range files {
		var err error
		if f.Existed {
			err = tool.AtomicWrite(f.Path, f.Data)
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
