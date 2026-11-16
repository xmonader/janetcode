package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// A fork or background subagent can write after the turn that started it
// has ended, when that turn's manifest is already recorded. Without more,
// its files joined the checkpoint but not its manifest, and a scoped
// `/undo turn n` read them as somebody else's edits and left them alone.
//
// A late call therefore records what it changed in the manifest of the
// checkpoint it joined. When the turn left no checkpoint at all (it
// changed nothing), the late call opens one of its own, marked Late, with
// a snapshot taken just before the call; /checkpoints lists it like any
// other.

// lateCall is a fork's mutating call made while the owner's turn is not
// running.
type lateCall struct {
	owner  *Agent
	turn   int
	opened bool           // the call opened its own checkpoint
	pre    *snapshot.Snap // the tree just before a shell call, to see what it changed
}

// lateBegin is called before a fork's mutating call, owner's turn being
// over. It lets the call's snapshot open a checkpoint when there is none
// to join, and returns the record end completes; after is called once
// the call's snapshot has been taken.
func (a *Agent) lateBegin(ctx context.Context, owner *Agent, t tool.Tool) (lc *lateCall, after func()) {
	owner.cp.mu.Lock()
	lc = &lateCall{owner: owner, turn: owner.cp.turn, opened: !owner.cp.open}
	if lc.opened {
		owner.cp.gitTried = false // so the call's snapshot opens a checkpoint
	}
	owner.cp.mu.Unlock()
	return lc, func() {
		owner.cp.mu.Lock()
		if lc.opened && owner.cp.open {
			owner.cp.all[len(owner.cp.all)-1].Late = true
		}
		owner.cp.mu.Unlock()
		if repo := owner.snaps(); repo != nil && (t.Name() == "bash" || t.Name() == "terminal") {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if snap, err := repo.Take(cctx, "late fork call"); err == nil {
				lc.pre = &snap
			}
			cancel()
		}
	}
}

// end records what a late call changed in the manifest of the checkpoint
// it joined. A nil lateCall (any other call) does nothing.
func (lc *lateCall) end(ctx context.Context, t tool.Tool, call llm.Block) {
	if lc == nil {
		return
	}
	owner := lc.owner
	ctx = context.WithoutCancel(ctx)
	seen := map[string]bool{}
	var entries []TouchedFile
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		tf := TouchedFile{Path: p}
		if _, err := os.Lstat(p); err == nil {
			tf.Sum = hashPath(p)
		}
		entries = append(entries, tf)
	}
	if tc, ok := t.(tool.Toucher); ok {
		if paths, err := tc.Touches(call.Input); err == nil {
			for _, p := range paths {
				add(p)
			}
		}
	}
	if lc.pre != nil {
		repo := owner.snaps()
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		lines, err := repo.Diff(cctx, *lc.pre)
		cancel()
		repo.Drop(ctx, *lc.pre)
		if err == nil {
			for _, l := range lines {
				if len(l) >= 3 {
					add(l[2:])
				}
			}
		}
	}
	owner.cp.mu.Lock()
	if owner.running.Load() || !owner.cp.open {
		// A new turn began: its own manifest will see the change.
		owner.cp.mu.Unlock()
		return
	}
	cur := &owner.cp.all[len(owner.cp.all)-1]
	if cur.Turn != lc.turn || !(cur.Manifested || cur.Late) {
		// Not a checkpoint whose manifest this call may extend: one
		// whose manifest could not be produced restores everything.
		owner.cp.mu.Unlock()
		return
	}
	for _, e := range entries {
		found := false
		for i := range cur.Touched {
			if cur.Touched[i].Path == e.Path {
				cur.Touched[i], found = e, true
				break
			}
		}
		if !found {
			cur.Touched = append(cur.Touched, e)
		}
	}
	cur.Manifested = true
	owner.cp.dirty = true
	owner.cp.mu.Unlock()
	if err := owner.SaveUndo(); err != nil {
		owner.debugf("save undo: %v", err)
	}
}
