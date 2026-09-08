package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
				c.all = append(c.all, Checkpoint{When: time.Now()})
				c.open = true
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
			c.all = append(c.all, Checkpoint{When: time.Now()})
			c.open = true
		}
		cur := &c.all[len(c.all)-1]
		cur.Files = append(cur.Files, snap)
		c.dirty = true
	}
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
	if len(a.cp.all) == 0 {
		err := os.Remove(a.UndoPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(a.cp.all)
	if err != nil {
		return err
	}
	return tool.AtomicWrite(a.UndoPath, data)
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
	var all []Checkpoint
	if err := json.Unmarshal(data, &all); err != nil {
		return fmt.Errorf("%s: %w", a.UndoPath, err)
	}
	a.cp.mu.Lock()
	a.cp.all, a.cp.open, a.cp.dirty = all, false, false
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
// listing files that could not be restored.
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
	defer func() {
		a.cp.mu.Unlock()
		if err := a.SaveUndo(); err != nil {
			a.debugf("save undo: %v", err)
		}
		a.cp.mu.Lock()
	}()
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
