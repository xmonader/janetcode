package agent

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// recordStart snapshots the work tree on a session's first user turn and
// stores it in the session's metadata, so the session can later be turned
// into a regression eval (/eval keep) that replays the exact tree and
// commit it began from. It runs once per session, only when the session's
// log is a real *session.Session (an eval's in-memory log is not) and the
// cwd is a git repository (Snapshots is then set by buildAgent). Failures
// are silent: the only consequence is that /eval keep later cannot keep
// this session.
func (a *Agent) recordStart() {
	if a.Snapshots == nil {
		return
	}
	sess, ok := a.Log.(*session.Session)
	if !ok || sess.Meta.Start != nil {
		return
	}
	// A context of its own, like captureState's: the turn's own context
	// may be cancelled before this ever gets a chance to run under it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := a.Snapshots.Take(ctx, "janetcode session start "+sess.Meta.ID)
	if err != nil {
		return
	}
	_ = sess.SetStart(session.StartState{Head: snap.Head, Snap: snap.Commit, At: time.Now().UTC()})
}
