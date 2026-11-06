package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The audit's file/change records come from the file tools, which name
// their paths. A shell command names none, so for bash the work tree is
// read around the call instead: git status before and after, and the
// digest of every file either listed. A file that changed across the call
// gets a record like a file tool's, with the call's id.
//
// Limits, which the manual repeats: only in a git repository (a shadow
// store for a directory outside git has no index to compare with); ignored
// files are not seen; a background job still writing after its bash call
// returned is attributed to the next bash call; and a status listing more
// than bashAuditMax files is skipped rather than hashed.

const (
	bashAuditMax     = 500
	bashAuditTimeout = 5 * time.Second
)

// bashAuditState is the dirty files' digests before a bash call.
type bashAuditState map[string]string

// bashAuditBefore reads the dirty files' digests before a bash call; nil
// when the call is not audited this way.
func (a *Agent) bashAuditBefore(ctx context.Context, u llm.Block) bashAuditState {
	if u.Name != "bash" || a.Log == nil {
		return nil
	}
	owner := a
	if a.UndoOwner != nil {
		owner = a.UndoOwner
	}
	repo := owner.userRepo()
	if repo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, bashAuditTimeout)
	defer cancel()
	paths, err := repo.Dirty(ctx)
	if err != nil || len(paths) > bashAuditMax {
		return nil
	}
	st := bashAuditState{}
	for _, p := range paths {
		st[p] = fileDigest(p)
	}
	return st
}

// bashAuditAfter records a file/change event for every file whose
// content differs from before the call.
func (a *Agent) bashAuditAfter(ctx context.Context, u llm.Block, before bashAuditState) {
	if before == nil {
		return
	}
	owner := a
	if a.UndoOwner != nil {
		owner = a.UndoOwner
	}
	repo := owner.userRepo()
	if repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bashAuditTimeout)
	defer cancel()
	now, err := repo.Dirty(ctx)
	if err != nil || len(now) > bashAuditMax {
		return
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range now {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for p := range before {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		after := fileDigest(p)
		prev, ok := before[p]
		var orig *bashOrigin
		switch {
		case !ok:
			// Clean before the call: its content was the index's.
			data, inIndex, err := repo.IndexContent(ctx, p)
			if err != nil {
				continue
			}
			prev = contentDigest(data, inIndex)
			orig = &bashOrigin{existed: inIndex, before: data}
		case prev == "absent":
			orig = &bashOrigin{}
		}
		if prev != after {
			if orig != nil {
				a.noteBashOrigin(p, *orig)
			}
			a.logEvent(session.Event{Type: session.TypeFileChange, CallID: u.ID, Name: p, Arguments: prev + "->" + after})
		}
	}
}

// fileDigest is contentDigest of the file on disk, streamed.
func fileDigest(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return "absent"
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return "absent"
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}
