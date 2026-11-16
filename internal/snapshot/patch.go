package snapshot

import (
	"context"
	"errors"
)

// Patch is the change from one snapshot to another, or to the work tree
// when to is nil: a diffstat followed by the unified patch, empty when
// the two are the same. It only reads, touching neither the work tree,
// HEAD nor the real index. Renames are not detected, so a moved file
// reads as a deletion and an addition, which is what a restore would do.
func (r *Repo) Patch(ctx context.Context, from Snap, to *Snap) (string, error) {
	if from.Commit == "" || (to != nil && to.Commit == "") {
		return "", errors.New("snapshot has no commit")
	}
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	var args []string
	if to != nil {
		args = []string{"diff", "--no-renames", "--no-color", "--stat", "-p", from.Commit, to.Commit}
	} else {
		if _, err := r.run(ctx, idx, "read-tree", from.Commit); err != nil {
			return "", err
		}
		if _, err := r.run(ctx, idx, "add", "-A", "--", "."); err != nil {
			return "", err
		}
		args = []string{"diff", "--cached", "--no-renames", "--no-color", "--stat", "-p", from.Commit}
	}
	out, err := r.run(ctx, idx, args...)
	if err != nil || out == "" {
		return "", err
	}
	// run trims the final newline, which a patch needs to apply.
	return out + "\n", nil
}
