package perm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// LearnAfter is how many times a call has to be approved, with no
// refusal in between, before its shape is offered as a rule to save.
const LearnAfter = 3

// Tally is how often a permission-rule shape (see Suggest) has been
// approved or refused, and when it last happened.
type Tally struct {
	Allowed int `json:"allowed"`
	Denied  int `json:"denied"`
	// Warned is set once a call of this shape was approved while it
	// carried a warning. Such a shape is never proposed as a rule: the
	// prompt does not offer it, so /permissions must not either.
	Warned bool      `json:"warned,omitempty"`
	Last   time.Time `json:"last"`
}

// Candidate is a shape the ledger thinks is worth saving as a rule: it
// has been approved Allowed times and never refused.
type Candidate struct {
	Rule string
	Tally
}

// Ledger remembers, per project and across sessions, how the user has
// answered permission prompts of each rule shape. It is the record
// "learned permissions" proposes a rule from; it never saves a rule
// itself, the r key and /permissions still do that.
//
// A Ledger with nowhere to keep its record — a nil *Ledger, or one from
// OpenLedger given an empty configDir or projectKey — is a valid no-op:
// every method reads and writes nothing, the same convention as
// hints.Store, so a caller never has to check before using one.
type Ledger struct {
	mu   sync.Mutex
	path string
	m    map[string]Tally
}

// OpenLedger returns the ledger for one project under the config
// directory. Nothing is read from disk until the first call that needs
// it.
func OpenLedger(configDir, projectKey string) *Ledger {
	if configDir == "" || projectKey == "" {
		return &Ledger{}
	}
	return &Ledger{path: filepath.Join(configDir, "permissions", projectKey+".json")}
}

// load reads the file the first time it is needed, so a ledger that is
// only ever asked about shapes nobody has answered yet never touches
// disk. An unreadable or malformed file just starts empty: the worst
// case is a rule offered again that had already been learned once.
// Called with mu held.
func (l *Ledger) load() map[string]Tally {
	if l.m != nil {
		return l.m
	}
	l.m = map[string]Tally{}
	if data, err := os.ReadFile(l.path); err == nil {
		_ = json.Unmarshal(data, &l.m)
		if l.m == nil {
			l.m = map[string]Tally{}
		}
	}
	return l.m
}

// Record notes one prompt answer for shape and returns its new tally.
// Write errors are ignored, like the hints store: the worst case of a
// failed write is one answer the next session does not remember.
func (l *Ledger) Record(shape string, allowed bool) Tally {
	if l == nil || l.path == "" || shape == "" {
		return Tally{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.load()
	t := m[shape]
	if allowed {
		t.Allowed++
	} else {
		t.Denied++
	}
	t.Last = time.Now()
	m[shape] = t
	if data, err := json.Marshal(m); err == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err == nil {
			_ = fsutil.WriteFileAtomic(l.path, data, 0o600)
		}
	}
	return t
}

// MarkWarned records that a call of shape carried a warning.
func (l *Ledger) MarkWarned(shape string) {
	if l == nil || l.path == "" || shape == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.load()
	t := m[shape]
	if t.Warned {
		return
	}
	t.Warned = true
	m[shape] = t
	if data, err := json.Marshal(m); err == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err == nil {
			_ = fsutil.WriteFileAtomic(l.path, data, 0o600)
		}
	}
}

// Get returns the tally for shape, zero if it has never been answered.
func (l *Ledger) Get(shape string) Tally {
	if l == nil || l.path == "" {
		return Tally{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load()[shape]
}

// Candidates lists shapes approved at least min times with no refusals,
// sorted by approvals descending then by name, for the "approved often,
// not saved" line in /permissions.
func (l *Ledger) Candidates(min int) []Candidate {
	if l == nil || l.path == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.load()
	out := make([]Candidate, 0, len(m))
	for shape, t := range m {
		if t.Allowed >= min && t.Denied == 0 && !t.Warned {
			out = append(out, Candidate{Rule: shape, Tally: t})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Allowed != out[j].Allowed {
			return out[i].Allowed > out[j].Allowed
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// CandidateLines is the "approved often, not saved" text for /permissions,
// shared by both front-ends: one line per shape the ledger has seen
// approved LearnAfter times with no refusals that is not already covered
// by a saved allow rule, naming the command that saves it.
func CandidateLines(ledger *Ledger, rules Rules) []string {
	var out []string
	for _, c := range ledger.Candidates(LearnAfter) {
		r, err := Parse(c.Rule)
		if err != nil || rules.hasAllow(r) {
			continue
		}
		out = append(out, fmt.Sprintf("/permissions allow %s (approved %d×, never refused)", c.Rule, c.Allowed))
	}
	return out
}
