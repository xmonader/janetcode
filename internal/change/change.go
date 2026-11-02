package change

import (
	"errors"
	"time"

	"github.com/xmonader/janetcode/internal/diff"
)

// Change is one file changed by one tool call: the unit of provenance.
// Before and After are the file's content around the call, so a single
// decision can be shown, blamed and reverted on its own after later
// changes landed on top.
type Change struct {
	Seq     int       `json:"seq"`
	Turn    int       `json:"turn"`
	Call    string    `json:"call"`
	Tool    string    `json:"tool"`
	Intent  string    `json:"intent"`
	When    time.Time `json:"when"`
	Path    string    `json:"path"`
	Existed bool      `json:"existed"`
	Before  []byte    `json:"before,omitempty"`
	After   []byte    `json:"after,omitempty"`
	// Gone says the change left no file at Path, which an empty After
	// alone cannot tell from a file emptied: an undo removed it, or a
	// turn restore did. Undoing such a change puts Before back.
	Gone   bool `json:"gone,omitempty"`
	Undone bool `json:"undone,omitempty"`
	// Attempt is the subagent attempt (see agent.AttemptStore) whose work
	// this change is, so /undo agent can unwind exactly that child's files.
	Attempt string `json:"attempt,omitempty"`
}

// Hunks is the change as a unified diff.
func (c Change) Hunks() []Hunk {
	return MakeHunks(DiffLines(diff.SplitLinesKeepEOL(string(c.Before)), diff.SplitLinesKeepEOL(string(c.After))))
}

// Stats counts the lines the change added and removed.
func (c Change) Stats() (add, del int) {
	for _, h := range c.Hunks() {
		a, d := h.Stats()
		add += a
		del += d
	}
	return add, del
}

// ErrNoSuchChange is returned for an unknown change number.
var ErrNoSuchChange = errors.New("no such change")

// ErrNoChange is returned by Blame when this session never changed the
// path, so a caller can fall back to committed history (see
// internal/proof.BlameCommitted) instead of matching the message text.
var ErrNoChange = errors.New("no change to this path in this session")

// Revert computes what an existing file's content would be if
// change c were reverted on top of cur (its current lines, e.g. from
// splitLines), without writing anything: each hunk is located in cur and
// put back the way it was, newest hunk first, same as change.UndoChange itself
// does — this is UndoChange's only implementation of that, factored out so
// a caller that wants to know the answer without writing (the undo
// coupling guard, see undo_coupling.go) can never drift from what actually
// happens on disk. ok is false when some hunk's lines could not be found
// (edited since c was recorded); badHunk is that hunk's 1-based index
// among c.Hunks() for the caller's own message, 0 when ok.
func Revert(cur []string, c Change) (reverted []string, ok bool, badHunk int) {
	lines := append([]string(nil), cur...)
	hunks := c.Hunks()
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		old, new := h.Sides()
		at, found := locate(lines, new, h.NewStart-1)
		if !found {
			return nil, false, i + 1
		}
		lines = append(append(append([]string{}, lines[:at]...), old...), lines[at+len(new):]...)
	}
	return lines, true, 0
}

// locate finds block in lines, preferring the occurrence nearest to hint.
func locate(lines, block []string, hint int) (int, bool) {
	if len(block) == 0 {
		return min(max(hint, 0), len(lines)), true
	}
	best, found := 0, false
	for i := 0; i+len(block) <= len(lines); i++ {
		match := true
		for j := range block {
			if lines[i+j] != block[j] {
				match = false
				break
			}
		}
		if match && (!found || abs(i-hint) < abs(best-hint)) {
			best, found = i, true
		}
	}
	return best, found
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
