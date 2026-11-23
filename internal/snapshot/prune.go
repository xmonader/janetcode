package snapshot

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Every Take anchors its commit under RefPrefix so gc keeps it, and nothing
// but Drop ever removed one: a turn that changed nothing drops its own, but
// every checkpoint that was kept lives on after its session is forgotten.
// A long-lived repository ended up with thousands of anchored commits, and
// gc could reclaim none of them. Prune is the janitor.

const (
	// DefaultPruneAge is how old an unreferenced snapshot ref must be to
	// be deleted. A week is longer than anyone leaves a session open
	// before asking for /undo, and short enough that abandoned runs go.
	DefaultPruneAge = 7 * 24 * time.Hour
	// DefaultPruneKeep caps the younger unreferenced refs: a burst of
	// headless runs, each leaving checkpoints nothing will ever undo,
	// must not wait a week to be bounded.
	DefaultPruneKeep = 200
	// DefaultSessionAge is how recently a session must have been modified
	// for the snapshots its undo record names to be kept regardless of
	// the limits above.
	DefaultSessionAge = 30 * 24 * time.Hour
)

// PrunePolicy says which unreferenced snapshot refs Prune deletes.
type PrunePolicy struct {
	// MaxAge deletes unreferenced refs older than this.
	MaxAge time.Duration
	// Keep is how many of the younger unreferenced refs survive, newest
	// first; the rest are deleted.
	Keep int
}

// DefaultPrunePolicy is the policy a session start uses when the
// configuration says nothing.
func DefaultPrunePolicy() PrunePolicy {
	return PrunePolicy{MaxAge: DefaultPruneAge, Keep: DefaultPruneKeep}
}

// Prune deletes snapshot refs that nothing refers to, and returns how many
// it deleted. A ref whose commit is in referenced is kept whatever its
// age: a saved session's undo record still points at it. Only refs under
// RefPrefix are listed, so no branch, tag or foreign ref can be touched.
// Deleting is best effort: a ref that cannot be deleted is skipped and the
// first error is returned beside the count.
func (r *Repo) Prune(ctx context.Context, referenced map[string]bool, p PrunePolicy, now time.Time) (int, error) {
	out, err := r.run(ctx, "", "for-each-ref", "--format=%(refname) %(committerdate:unix)", RefPrefix)
	if err != nil {
		return 0, err
	}
	type ref struct {
		name string
		when time.Time
	}
	var young []ref
	var doomed []string
	for _, line := range strings.Split(out, "\n") {
		name, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, RefPrefix) {
			continue
		}
		secs, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		if referenced[strings.TrimPrefix(name, RefPrefix)] {
			continue
		}
		when := time.Unix(secs, 0)
		if p.MaxAge > 0 && now.Sub(when) > p.MaxAge {
			doomed = append(doomed, name)
			continue
		}
		young = append(young, ref{name, when})
	}
	sort.SliceStable(young, func(a, b int) bool { return young[a].when.After(young[b].when) })
	if p.Keep >= 0 && len(young) > p.Keep {
		for _, y := range young[p.Keep:] {
			doomed = append(doomed, y.name)
		}
	}
	var first error
	deleted := 0
	for _, name := range doomed {
		if ctx.Err() != nil {
			if first == nil {
				first = ctx.Err()
			}
			break
		}
		if _, err := r.run(ctx, "", "update-ref", "-d", name); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		deleted++
	}
	return deleted, first
}
