package feature

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// Usage counts which catalogue features this user has reached for.
//
// It exists so that janetcode can stop telling somebody about a feature
// they use every day, and can tell them about the five they have never
// touched. That is all it is for.
//
// It never leaves the machine. There is no endpoint, no identifier and
// no timestamp: the file is a map from feature name to a count, in the
// config directory, and nothing in janetcode reads it except the hint
// rules and /stats. Counting happens in memory — a map increment on an
// event the terminal was already handling — and the file is written once,
// at exit.
type Usage struct {
	path string

	mu     sync.Mutex
	counts map[string]int
	dirty  bool
}

const usageFile = "usage.json"

// OpenUsage reads the counts from the config directory. An unreadable or
// missing file means "nothing used yet", which is the right answer for a
// first run and a harmless one for a corrupted file.
func OpenUsage(configDir string) *Usage {
	u := &Usage{counts: map[string]int{}}
	if configDir == "" {
		return u
	}
	u.path = filepath.Join(configDir, usageFile)
	data, err := os.ReadFile(u.path)
	if err != nil {
		return u
	}
	var counts map[string]int
	if json.Unmarshal(data, &counts) == nil && counts != nil {
		u.counts = counts
	}
	return u
}

// Note records one use. It is called from event handlers — a command
// dispatched, a key pressed, a file attached — so it does a map write
// and nothing else.
func (u *Usage) Note(name string) {
	if u == nil || name == "" {
		return
	}
	u.mu.Lock()
	u.counts[name]++
	u.dirty = true
	u.mu.Unlock()
}

// Used reports whether a feature has ever been used.
func (u *Usage) Used(name string) bool { return u.Count(name) > 0 }

// Count is how often a feature has been used.
func (u *Usage) Count(name string) int {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[name]
}

// Flush writes the counts, once, at exit. It is a no-op when nothing
// was used and when there is nowhere to write.
func (u *Usage) Flush() error {
	if u == nil || u.path == "" {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(u.counts)
	if err != nil {
		return err
	}
	u.dirty = false
	// Rewritten whole, so it is replaced by rename: a crash during the
	// flush at exit must not leave a truncated file that the next run
	// then fails to parse and starts counting from zero.
	return fsutil.AtomicWriteMode(u.path, data, fsutil.PrivateFileMode)
}

// Path is where the counts live, so /stats can say so rather than
// leaving the user to wonder what is being recorded.
func (u *Usage) Path() string {
	if u == nil {
		return ""
	}
	return u.path
}

// NotTried returns up to n catalogue features the user has never used,
// most useful first. Flags, tools and configuration keys are left out:
// the point is to name something you could type next, and "you have
// never used --eval-facts-blind" is not that.
func (u *Usage) NotTried(c *Catalog, n int) []Feature {
	if c == nil || n <= 0 {
		return nil
	}
	var out []Feature
	for _, f := range c.All() {
		switch f.Kind {
		case KindCommand, KindKey, KindBehave:
		default:
			continue
		}
		if u.Used(f.Name) || u.Used(f.Invocation()) {
			continue
		}
		if skipInNotTried[f.Name] {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return worth[out[i].Name] > worth[out[j].Name] })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// skipInNotTried are features nobody needs to be told about: either
// everybody already knows them, or the advice is circular.
var skipInNotTried = map[string]bool{
	"/help": true, "/quit": true, "/clear": true, "esc": true, "ctrl+c": true,
	"/hints": true,
}

// worth ranks what is most worth discovering, for the "not yet tried"
// line. Everything unlisted ranks zero and comes out in catalogue order,
// which is alphabetical and therefore arbitrary but stable.
var worth = map[string]int{
	"/verify": 90, "/undo": 85, "@path": 80, "ctrl+g": 78, "/goal": 75,
	"/remember": 72, "/plan": 70, "!command": 68, "/pin": 65, "/context": 62,
	"/changes": 60, "/diff": 58, "r": 56, "/resume": 54, "/permissions": 52,
	"/why": 50, "/inspect": 48, "ctrl+s": 46, "/fork": 44, "/skills": 42,
}

// Summary is the "not yet tried" line for /stats.
func (u *Usage) Summary(c *Catalog) string {
	rest := u.NotTried(c, 5)
	if len(rest) == 0 {
		return ""
	}
	// The bare name, not the invocation: "/verify [set <cmd>|mutate
	// <test cmd>|clear|trust] · /undo [preview|n] · @path · ctrl+g ·
	// /goal [objective|pause|resume|done]" wrapped onto two lines and
	// read as syntax to learn rather than as five things to try.
	var names []string
	for _, f := range rest {
		names = append(names, f.Name)
	}
	return "not yet tried: " + strings.Join(names, " · ") + " · /help <topic> explains any of them"
}
