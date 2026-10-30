// Managing stored sessions from a list: rename, archive, star, delete.
// The session being run is changed through its own *Session; these act on
// the others by id, refusing any a live process holds.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// ErrHeld is wrapped by a refusal to change a session a process holds.
type ErrHeld struct{ ID string }

func (e ErrHeld) Error() string { return "session " + e.ID + " is open in a running janetcode" }

func pathOf(dir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(dir, id+".jsonl"), nil
}

// ModifyMeta changes the metadata of the stored session id and keeps its
// file time, so renaming or starring a session does not reorder the list
// as if it had been used. A session a live process holds is refused.
func ModifyMeta(dir, id string, fn func(*Meta)) error {
	path, err := pathOf(dir, id)
	if err != nil {
		return err
	}
	if _, held := Holder(path); held {
		return ErrHeld{id}
	}
	s, _, err := Open(dir, id)
	if err != nil {
		return err
	}
	st, serr := os.Stat(path)
	if err := s.UpdateMeta(fn); err != nil {
		return err
	}
	if serr == nil {
		_ = os.Chtimes(path, st.ModTime(), st.ModTime())
	}
	return nil
}

// Delete removes the stored session id and its sidecar files (undo
// checkpoints, lock, pid, background status). A held session is refused.
func Delete(dir, id string) error {
	path, err := pathOf(dir, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	if _, held := Holder(path); held {
		return ErrHeld{id}
	}
	base := strings.TrimSuffix(path, ".jsonl")
	if err := os.Remove(path); err != nil {
		return err
	}
	side, _ := filepath.Glob(base + ".*")
	for _, f := range side {
		_ = os.Remove(f)
	}
	return nil
}

// RealUser reports whether m is something the person typed, as against a
// tool result or text the harness attached (the repository map, notes).
func RealUser(m llm.Message) bool {
	if m.Role != llm.RoleUser {
		return false
	}
	typed := false
	for _, b := range m.Blocks {
		switch {
		case b.Type == llm.BlockToolResult:
			return false
		case b.Type == llm.BlockText && !b.Synthetic && !strings.HasPrefix(b.Text, "[repository map:"):
			typed = true
		}
	}
	return typed
}

// Where a message cut falls relative to the n-th typed message.
const (
	CutBefore  = iota // before the message: the message is dropped
	CutAfter          // right after the message
	CutEndTurn        // after the whole turn the message starts
)

// MessageCut finds the seq a fork or rewind cuts at for the n-th typed
// message (1-based) of the live conversation. A cut of 0 with ok true
// means "keep everything" (the message ends the conversation).
func MessageCut(events []Event, n, where int) (cutSeq int, ok bool) {
	msgs := FoldSurface(events)
	seqs := CurrentSeqs(events)
	if len(msgs) != len(seqs) {
		return 0, false
	}
	count := 0
	for k, m := range msgs {
		if !RealUser(m) {
			continue
		}
		if count++; count != n {
			continue
		}
		switch where {
		case CutBefore:
			return seqs[k], true
		case CutAfter:
			if k+1 < len(seqs) {
				return seqs[k+1], true
			}
			return 0, true
		}
		for j := k + 1; j < len(msgs); j++ {
			if RealUser(msgs[j]) {
				return seqs[j], true
			}
		}
		return 0, true
	}
	return 0, false
}
