package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/tool"
)

// Snapshot is a file as it was before a turn changed it.
type Snapshot struct {
	Path    string `json:"path"`
	Data    []byte `json:"data,omitempty"`
	Existed bool   `json:"existed"`
}

// Checkpoint groups the files a single turn modified through file tools.
// Shell commands are not tracked.
type Checkpoint struct {
	When  time.Time  `json:"when"`
	Files []Snapshot `json:"files"`
}

// ErrNothingToUndo is returned by Undo when no turn changed any file.
var ErrNothingToUndo = errors.New("nothing to undo")

// checkpointer records pre-change snapshots for the running turn.
type checkpointer struct {
	mu    sync.Mutex
	open  bool // the last checkpoint in all belongs to the running turn
	dirty bool // changed since the last SaveUndo
	all   []Checkpoint
}

// beginTurn closes the previous turn's checkpoint; the next snapshot opens
// a new one so turns without file changes leave nothing behind.
func (c *checkpointer) beginTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open = false
}

// snapshot saves the current state of every path the tool will touch, once
// per turn per path.
func (c *checkpointer) snapshot(t tool.Tool, input []byte) {
	tc, ok := t.(tool.Toucher)
	if !ok {
		return
	}
	paths, err := tc.Touches(input)
	if err != nil {
		return // the tool itself will report the bad input
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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

// saveUndoIfDirty is called at the end of a turn.
func (a *Agent) saveUndoIfDirty() {
	a.cp.mu.Lock()
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
