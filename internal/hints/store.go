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

// Shown reads the ids already given for this project. A missing or
// unreadable file means "none": a hint repeated once is a smaller
// failure than a session that will not start.
func (s *Store) Shown() []string {
	if s == nil || s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var f file
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Shown
}

// Save records the whole set. It is called when a hint fires, so the
// record survives a session that ends unexpectedly.
func (s *Store) Save(ids []string) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(file{Shown: ids})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
