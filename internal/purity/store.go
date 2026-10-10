package purity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// PromoteAfter is how many clean, promotable runs a command needs in one
// repository before it runs without asking there.
const PromoteAfter = 3

// Record is what has been seen of one command line in one repository.
type Record struct {
	// Pure counts the runs the monitor saw change nothing.
	Pure int `json:"pure"`
	// Impure, once set, says what a run changed. It never clears: a
	// command seen writing once is not a read-only command.
	Impure string `json:"impure,omitempty"`
	// Barred, once set, says why the command cannot be promoted however
	// clean its runs are (it runs code it reads).
	Barred string    `json:"barred,omitempty"`
	Last   time.Time `json:"last"`
}

// Promoted reports whether the record lets the command run unasked.
func (r Record) Promoted() bool {
	return r.Impure == "" && r.Barred == "" && r.Pure >= PromoteAfter
}

// Store keeps the records, one file per repository, under a directory of
// the user's configuration. A nil *Store is a valid no-op that promotes
// nothing, the convention perm.Ledger follows, so a caller with the
// feature off never checks.
type Store struct {
	mu   sync.Mutex
	dir  string
	maps map[string]map[string]Record
}

// Open returns the store under configDir, or nil when on is false or
// there is nowhere to keep it.
func Open(on bool, configDir string) *Store {
	if !on || configDir == "" {
		return nil
	}
	return &Store{dir: filepath.Join(configDir, "purity")}
}

// Key is the command line a record is kept under: the text as the model
// sent it, with the surrounding space trimmed. Nothing looser: `grep -r x`
// and `grep -r y` are two commands, and two commands are two records.
func Key(command string) string { return strings.TrimSpace(command) }

// file is the record file for one repository, named by the hash of its
// absolute path: two clones of one project are two repositories here,
// since what one runs is not what the other runs.
func (s *Store) file(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:12])+".json")
}

// load reads one repository's records the first time they are needed. An
// unreadable or malformed file starts empty, which promotes nothing.
// Called with mu held.
func (s *Store) load(root string) map[string]Record {
	if s.maps == nil {
		s.maps = map[string]map[string]Record{}
	}
	if m, ok := s.maps[root]; ok {
		return m
	}
	m := map[string]Record{}
	if data, err := os.ReadFile(s.file(root)); err == nil {
		_ = json.Unmarshal(data, &m)
		if m == nil {
			m = map[string]Record{}
		}
	}
	s.maps[root] = m
	return m
}

// Get returns the record for command in root, zero when there is none.
func (s *Store) Get(root, command string) Record {
	if s == nil || root == "" {
		return Record{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(root)[Key(command)]
}

// Promoted reports whether command runs unasked in root.
func (s *Store) Promoted(root, command string) bool {
	return s.Get(root, command).Promoted()
}

// Observe records one traced run and returns the record before and after.
// A run that proves nothing (a trace that could not be read) is not
// recorded at all.
func (s *Store) Observe(root, command string, v Verdict) (before, after Record) {
	if s == nil || root == "" || Key(command) == "" || v.Blind {
		return Record{}, Record{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load(root)
	k := Key(command)
	before = m[k]
	after = before
	switch ok, why := Promotable(v, root); {
	case !v.Pure:
		after.Impure = why
	case !ok:
		after.Barred = why
	default:
		after.Pure++
	}
	after.Last = time.Now()
	m[k] = after
	if data, err := json.Marshal(m); err == nil {
		if err := os.MkdirAll(s.dir, 0o700); err == nil {
			_ = fsutil.WriteFileAtomic(s.file(root), data, 0o600)
		}
	}
	return before, after
}
