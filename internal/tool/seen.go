package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

// Seen remembers the content hash of every file the agent last read or
// wrote, so an edit can refuse to proceed when the file changed under it
// (the user edited it, a formatter ran, another session touched it).
// A nil *Seen disables the check.
type Seen struct {
	mu     sync.Mutex
	hashes map[string]string
	// reads is the read-set: the hash of each file the first time a read
	// tool showed it, kept apart from hashes because a write moves those
	// on. An editing subagent's merge-back checks it against the project
	// to find what changed under the subagent (see internal/occ).
	reads map[string]string
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Record notes what the file looked like the last time the agent saw it.
func (s *Seen) Record(path string, data []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes == nil {
		s.hashes = map[string]string{}
	}
	s.hashes[path] = hashBytes(data)
}

// RecordRead is Record for a read tool: it also enters the file in the
// read-set, once, with the content the agent first saw.
func (s *Seen) RecordRead(path string, data []byte) {
	if s == nil {
		return
	}
	h := hashBytes(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes == nil {
		s.hashes = map[string]string{}
	}
	s.hashes[path] = h
	if s.reads == nil {
		s.reads = map[string]string{}
	}
	if _, ok := s.reads[path]; !ok {
		s.reads[path] = h
	}
}

// Reads is a copy of the read-set: every path a read tool showed, with
// the hash of what it first showed.
func (s *Seen) Reads() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.reads))
	for p, h := range s.reads {
		out[p] = h
	}
	return out
}

// HashBytes is the hash Seen records, for a caller comparing content
// against a read-set entry.
func HashBytes(b []byte) string { return hashBytes(b) }

// Forget drops a path (after the file is deleted, or on undo).
func (s *Seen) Forget(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.hashes, path)
	s.mu.Unlock()
}

// Read reports whether the agent has seen this file in this session.
func (s *Seen) Read(path string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.hashes[path]
	return ok
}

// Cause says why the file the agent is about to change is not the file it
// last saw. Each one needs a different thing done about it, so each gets
// its own message rather than one "read it again".
type Cause int

const (
	// CauseChanged: read earlier, different content now.
	CauseChanged Cause = iota
	// CauseDisappeared: read earlier, not there now.
	CauseDisappeared
	// CauseNeverRead: the agent has not looked at this file at all.
	CauseNeverRead
	// CauseDirectory: the path names a directory.
	CauseDirectory
	// CauseBinary: the file is not text.
	CauseBinary
)

// ErrPriorRead is a refusal to change a file the agent has not established
// the current contents of. Verb is "edit" or "write": the guidance differs,
// because an edit only has to match the region it replaces while a write
// replaces everything.
type ErrPriorRead struct {
	Path  string
	Verb  string
	Cause Cause
}

// reread is the verb-specific instruction that ends most of the messages.
func (e *ErrPriorRead) reread() string {
	if e.Verb == "write" {
		// write_file replaces the file, so a window of it is not enough:
		// whatever is outside the window is what gets thrown away.
		return "Read the whole file with read_file before writing it; write_file replaces everything, so a partial read is not enough here."
	}
	return "Read it again with read_file before editing — a partial read (offset/limit) around the part you are changing is enough."
}

func (e *ErrPriorRead) Error() string {
	switch e.Cause {
	case CauseDisappeared:
		return fmt.Sprintf("%s was read earlier in this session but is not there now: it was deleted, moved or renamed. Find out where it went before you %s it; writing would re-create it at the old path and leave the new one stale.", e.Path, e.Verb)
	case CauseNeverRead:
		return fmt.Sprintf("%s already exists and has not been read in this session, so writing it would replace content you have not seen. %s", e.Path, e.reread())
	case CauseDirectory:
		return fmt.Sprintf("%s is a directory, not a file. list_dir shows what is in it and glob finds files by pattern; name a file inside it to %s.", e.Path, e.Verb)
	case CauseBinary:
		done := "edited"
		if e.Verb == "write" {
			done = "written"
		}
		return fmt.Sprintf("%s is a binary file (a NUL byte in its first 4 KB), so it cannot be %s as text. Use bash if you really have to change it.", e.Path, done)
	default:
		return fmt.Sprintf("%s changed on disk since you last read it: the user edited it, a formatter ran, or another session touched it. %s", e.Path, e.reread())
	}
}

// ErrChanged is the old single-cause error, kept as the name callers test
// for; it is now ErrPriorRead with CauseChanged.
type ErrChanged = ErrPriorRead

// CheckAt compares the file's current content with what was recorded and
// reports what is wrong with it, if anything. verb is "edit" or "write";
// ws says which machine to look on, so a remote file is not probed on
// this one.
//
// A file the agent has never read passes for an edit: an edit names the
// exact text it replaces, so it cannot silently destroy something it did
// not see, and the coach supplies the content when one misses. A write
// does not name anything, so overwriting an unread file is refused.
func (s *Seen) CheckAt(ctx context.Context, ws *Workspace, path, display, verb string) error {
	if s == nil {
		return nil
	}
	st, statErr := statOf(ctx, ws, path)
	if statErr == nil && st.IsDir() {
		return &ErrPriorRead{Path: display, Verb: verb, Cause: CauseDirectory}
	}
	s.mu.Lock()
	want, ok := s.hashes[path]
	s.mu.Unlock()
	if !ok {
		if verb == "write" && statErr == nil {
			return &ErrPriorRead{Path: display, Verb: verb, Cause: CauseNeverRead}
		}
		return nil
	}
	data, err := readFileOf(ctx, ws, path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ErrPriorRead{Path: display, Verb: verb, Cause: CauseDisappeared}
		}
		return nil // unreadable: the write will report the real problem
	}
	if isBinary(data) {
		return &ErrPriorRead{Path: display, Verb: verb, Cause: CauseBinary}
	}
	if hashBytes(data) != want {
		return &ErrPriorRead{Path: display, Verb: verb, Cause: CauseChanged}
	}
	return nil
}
