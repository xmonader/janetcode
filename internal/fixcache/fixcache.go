// Package fixcache remembers, per repository, which change made an error
// go away. A check that came back red names its error in a few lines; once
// a later run of the same check is green, the lines the red run named are
// normalised into signatures (Signatures) and stored with the diff of the
// files changed in between (Entry). When one of those signatures shows up
// again in the same repository, in a later session, the earlier fix can be
// put beside the error with no model call to retrieve it.
//
// A signature is one error line with the parts that change from run to run
// taken out: the repository's own path, temporary directories, line and
// column numbers, addresses and other numbers. Lines that say only that
// something failed ("FAIL", "exit status 1", "2 failed, 3 passed") are not
// signatures: they recur in every red run of every project and would
// attach a fix to errors that have nothing to do with it.
//
// It imports only the standard library and internal/diff, and does no I/O
// beyond the one JSON file a Store reads and writes.
package fixcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Limits: a red output yields at most MaxSignatures signatures, a store
// keeps at most MaxEntries entries (the oldest go first), and an entry's
// fix is cut to MaxFixBytes.
const (
	MaxSignatures = 4
	MaxEntries    = 64
	MaxFixBytes   = 2000
	maxLineBytes  = 200
)

// errorLineRe is a line that names an error rather than reporting
// progress: a compiler's or interpreter's error, an exception, a failed
// assertion with its operands.
var errorLineRe = regexp.MustCompile(`(?i)(\berror\b|error:|exception|panic:|fatal|undefined|not defined|has no attribute|cannot |can't |unable to|no such|not found|unexpected|mismatch|is not a |missing|expected .* got|assert)`)

// genericRe is an error line that names no particular error, after
// normalisation.
var genericRe = regexp.MustCompile(`(?i)^(` +
	`[-=*_ ]*(fail(ed|ure)?|error|errors|e|ok)?[-=*_ :]*` + // banners, bare verdicts
	`|.*\bexit (status|code) N\b.*` +
	`|.*\bN (failed|passed|errors?|failures?|skipped)\b.*` +
	`|.*\b(failures|errors)=N\b.*` +
	`|make(\[N\])?: \*\*\*.*` +
	`|npm err!.*` +
	`|error: (process|command|script|test failed|could not compile|aborting due to).*` +
	`|.*short test summary.*` +
	`|tests? failed.*` +
	`|-+ fail: \S+( \(.*\))?` +
	`|traceback \(most recent call last\):` +
	`|during handling of the above exception.*` +
	`)$`)

var (
	ansiRe    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	tmpRe     = regexp.MustCompile(`(/private)?/(tmp|var/folders)/\S*`)
	hexRe     = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	posRe     = regexp.MustCompile(`(\.\w+):\d+(:\d+)?`)
	numRe     = regexp.MustCompile(`\d+(\.\d+)?`)
	spaceRe   = regexp.MustCompile(`\s+`)
	wordRe    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	harnessRe = regexp.MustCompile(`^\[(exit code|harness|output is|hook:)`)
)

// Normalize is one output line as a signature, or "" when the line names
// no particular error. root is the repository's path, which is taken out
// so the same error in two checkouts has one signature.
func Normalize(line, root string) string {
	s := ansiRe.ReplaceAllString(line, "")
	s = strings.TrimSpace(s)
	if s == "" || harnessRe.MatchString(s) || !errorLineRe.MatchString(s) {
		return ""
	}
	if root != "" && root != "/" {
		s = strings.ReplaceAll(s, strings.TrimRight(root, "/")+"/", "")
		s = strings.ReplaceAll(s, root, ".")
	}
	s = tmpRe.ReplaceAllString(s, "<tmp>")
	s = posRe.ReplaceAllString(s, "$1")
	s = numRe.ReplaceAllString(hexRe.ReplaceAllString(s, "\x00"), "N")
	s = strings.ReplaceAll(s, "\x00", "ADDR")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if genericRe.MatchString(s) || len(wordRe.FindAllString(s, -1)) < 2 {
		return ""
	}
	if len(s) > maxLineBytes {
		s = s[:maxLineBytes]
	}
	return s
}

// Signatures are the distinct signatures of a red run's output, in the
// order they appear, at most MaxSignatures.
func Signatures(output, root string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(output, "\n") {
		s := Normalize(line, root)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == MaxSignatures {
			break
		}
	}
	return out
}

// Entry is one error the repository has seen go away.
type Entry struct {
	// Signatures are the red run's signatures.
	Signatures []string `json:"signatures"`
	// Command is the check that was red and then green.
	Command string `json:"command"`
	// Fix is the diff of the files changed from the red run to the green
	// one, cut to MaxFixBytes.
	Fix string `json:"fix"`
	// Files are the paths the fix changed, relative to the repository.
	Files []string `json:"files"`
	// Session tells one session's entries from another's: a hit in the
	// session that recorded the fix tells the model what it just did.
	Session string    `json:"session"`
	When    time.Time `json:"when"`
}

// Store is one repository's entries, kept in one JSON file.
type Store struct {
	Path    string  `json:"-"`
	Entries []Entry `json:"entries"`
}

// PathFor is the store file for the repository at root under dir.
func PathFor(dir, root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".json")
}

// Open reads the store at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{Path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return &Store{Path: path}, err
	}
	return s, nil
}

// Save writes the store, through a temporary file so a reader never sees
// half of it.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Add records an entry, replacing an older one with the same signatures
// and dropping the oldest past MaxEntries. An entry with no signatures or
// no fix is not recorded.
func (s *Store) Add(e Entry) bool {
	if len(e.Signatures) == 0 || strings.TrimSpace(e.Fix) == "" {
		return false
	}
	if len(e.Fix) > MaxFixBytes {
		e.Fix = e.Fix[:MaxFixBytes] + "\n…"
	}
	key := sigKey(e.Signatures)
	kept := s.Entries[:0]
	for _, old := range s.Entries {
		if sigKey(old.Signatures) != key {
			kept = append(kept, old)
		}
	}
	s.Entries = append(kept, e)
	if len(s.Entries) > MaxEntries {
		s.Entries = s.Entries[len(s.Entries)-MaxEntries:]
	}
	return true
}

// Lookup is the newest entry from another session that shares a
// signature with sigs, and the signature they share; ok is false when
// there is none.
func (s *Store) Lookup(sigs []string, session string) (e Entry, shared string, ok bool) {
	want := map[string]bool{}
	for _, g := range sigs {
		want[g] = true
	}
	for i := len(s.Entries) - 1; i >= 0; i-- {
		c := s.Entries[i]
		if c.Session == session {
			continue
		}
		for _, g := range c.Signatures {
			if want[g] {
				return c, g, true
			}
		}
	}
	return Entry{}, "", false
}

func sigKey(sigs []string) string {
	c := append([]string(nil), sigs...)
	sort.Strings(c)
	return strings.Join(c, "\x00")
}
