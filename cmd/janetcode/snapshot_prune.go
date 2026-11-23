package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// startSnapshotPrune deletes the snapshot refs old sessions left behind
// (snapshot.Repo.Prune) once per session start. It runs in the background
// and is best effort: the session never waits for it, and a failure goes to
// the debug log, not to the user, who asked for none of it. A shadow store
// is pruned like a repository; it is janitored by the same rules.
func startSnapshotPrune(repo *snapshot.Repo, sessDir string, cfg *config.Config, debug io.Writer) {
	if repo == nil || sessDir == "" {
		return
	}
	days := func(n int, def time.Duration) time.Duration {
		if n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
		return def
	}
	policy := snapshot.PrunePolicy{MaxAge: days(cfg.SnapshotKeepDays, snapshot.DefaultPruneAge), Keep: snapshot.DefaultPruneKeep}
	if cfg.SnapshotKeepCount > 0 {
		policy.Keep = cfg.SnapshotKeepCount
	}
	sessionAge := days(cfg.SnapshotSessionDays, snapshot.DefaultSessionAge)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		now := time.Now()
		n, err := repo.Prune(ctx, session.SnapshotRefs(sessDir, now.Add(-sessionAge)), policy, now)
		if debug == nil || (n == 0 && err == nil) {
			return
		}
		msg := fmt.Sprintf("snapshot prune: deleted %d ref(s)", n)
		if err != nil {
			msg += ": " + err.Error()
		}
		fmt.Fprintf(debug, "%s %s\n", now.Format("15:04:05.000"), msg)
	}()
}
