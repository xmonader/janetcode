package tool

import (
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

// Forget drops a path (after the file is deleted, or on undo).
func (s *Seen) Forget(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.hashes, path)
	s.mu.Unlock()
}

// ErrChanged is returned by Check when the file differs from what the
// agent last saw.
type ErrChanged struct{ Path string }

func (e *ErrChanged) Error() string {
	return fmt.Sprintf("%s changed on disk since you last read it (edited outside this session?). Read it again before changing it.", e.Path)
}

// Check compares the file's current content with what was recorded; a
// path never seen passes.
func (s *Seen) Check(path, display string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	want, ok := s.hashes[path]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ErrChanged{Path: display}
		}
		return nil // unreadable: the write will report the real problem
	}
	if hashBytes(data) != want {
		return &ErrChanged{Path: display}
	}
	return nil
}
