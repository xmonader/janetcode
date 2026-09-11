package hints

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The record of what has been said lives in the config directory, keyed
// by project, and never in the project itself: a hint janetcode showed
// you is not something to commit, and writing into somebody's
// repository uninvited is the thing this whole round is trying not to
// do.
//
// The file is read once at startup and written when a hint fires, which
// happens at most a handful of times in the life of a project. Neither
// is on the render path.

// Store persists hint state for one project.
type Store struct {
	path string
}

type file struct {
	Shown []string `json:"shown"`
	// Skipped are rules that were spent without being said: the feature
	// was already in use, or the first-run line had proposed the same
	// thing. They suppress the rule exactly like a shown one, but
	// counting them as hints given misreports what the user was told.
	// A file written before this field existed has none, and everything
	// in it reads as shown, which is what it meant at the time.
	Skipped []string `json:"skipped,omitempty"`
}

// Open returns the store for a project under the config directory. A
// missing directory is created lazily, on the first write.
func Open(configDir, projectKey string) *Store {
	if configDir == "" || projectKey == "" {
		return &Store{}
	}
	return &Store{path: filepath.Join(configDir, "hints", projectKey+".json")}
}

// Path is where the record is kept, or "" when there is nowhere to keep
// it. Exported so /hints can say where it went.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads the ids already spent for this project, split into the ones
// that were said and the ones that were silently skipped. A missing or
// unreadable file means "none": a hint repeated once is a smaller
// failure than a session that will not start.
func (s *Store) Load() (shown, skipped []string) {
	if s == nil || s.path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil
	}
	var f file
	if json.Unmarshal(data, &f) != nil {
		return nil, nil
	}
	return f.Shown, f.Skipped
}

// Shown reads the ids that were actually said for this project.
func (s *Store) Shown() []string {
	shown, _ := s.Load()
	return shown
}

// Save records the whole set. It is called when a hint fires, so the
// record survives a session that ends unexpectedly.
func (s *Store) Save(shown, skipped []string) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(file{Shown: shown, Skipped: skipped})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
