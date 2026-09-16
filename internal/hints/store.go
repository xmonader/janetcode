package hints

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// maxIDs bounds what one project's record may hold. There are fewer
// than twenty rules; a file naming hundreds of ids is corrupt or has
// been written by something else, and reading all of it into a map for
// the life of the process is not worth it. The first maxIDs are kept,
// so a plausible file is unaffected.
const maxIDs = 256

// Open returns the store for a project under the config directory. A
// missing directory is created lazily, on the first write.
//
// The key names one file inside the hints directory and nothing else:
// a key carrying a path separator, or made of nothing but dots, would
// write outside it, so it is refused rather than sanitised into some
// other project's record.
func Open(configDir, projectKey string) *Store {
	if configDir == "" || projectKey == "" || !safeKey(projectKey) {
		return &Store{}
	}
	return &Store{path: filepath.Join(configDir, "hints", projectKey+".json")}
}

// safeKey reports whether the key is a single harmless file name.
func safeKey(k string) bool {
	if strings.ContainsAny(k, `/\`) || strings.ContainsRune(k, os.PathSeparator) {
		return false
	}
	return strings.Trim(k, ".") != ""
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
	return clean(f.Shown), clean(f.Skipped)
}

// clean drops empty ids and bounds the list: what comes back is a file
// on disk, which may have been truncated, hand-edited or written by an
// older build.
func clean(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if len(out) == maxIDs {
			break
		}
		out = append(out, id)
	}
	return out
}

// Save records the whole set. It is called when a hint fires, so the
// record survives a session that ends unexpectedly.
//
// The write goes to a temporary file in the same directory and is
// renamed over the record, so a session killed mid-write leaves the
// previous record intact rather than a half-written file that reads as
// "nothing has been said" and repeats every hint. Two sessions in the
// same project race to rename; one of them wins whole, which is the
// worst that can happen and is what the unsynchronised writer did
// anyway.
func (s *Store) Save(shown, skipped []string) error {
	if s == nil || s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(file{Shown: shown, Skipped: skipped})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hints-*.json")
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600, which is the mode the record wants.
	defer os.Remove(tmp.Name()) // no-op once the rename has happened
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
