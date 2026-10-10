package agent

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/fixcache"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The error-signature fix cache (FixCache): when one of the model's checks
// comes back red, the error lines it names are kept as signatures
// (internal/fixcache); when a later run of the same command is green and
// files changed in between, the diff of those files is stored with the
// signatures, per repository, under the data directory. When a red run in
// a later session names a stored signature, the fix that made it go away
// last time is appended to the output. No model is asked to remember it
// or to retrieve it.
//
// It is opt-in (config fix_cache): it writes a file the user did not ask
// for and changes what the model reads, and the offline pre-screen found
// almost nothing to attach (see TestFixCachePrescreen). JANETCODE_OFF=fixcache
// turns it off again.

// fixCacheState is the session's open red runs and its identity in the
// store.
type fixCacheState struct {
	mu      sync.Mutex
	session string
	// pending maps a red command to its signatures and the last change
	// number before it: the changes after it, up to a green run of the
	// same command, are its fix.
	pending map[string]fixCachePending
	// told holds the signatures already answered this session, so one
	// error rerun ten times carries the earlier fix once.
	told map[string]bool
}

type fixCachePending struct {
	sigs  []string
	after int
}

func (s *fixCacheState) id() string {
	if s.session == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		s.session = hex.EncodeToString(b[:])
	}
	return s.session
}

// SetFixCache turns the fix cache on or off for the turns that follow.
func (a *Agent) SetFixCache(on bool) {
	a.featMu.Lock()
	a.FixCache = on
	a.featMu.Unlock()
}

// FixCacheOn is FixCache with a store to keep it in, safe to read while a
// turn runs, and off when JANETCODE_OFF names fixcache.
func (a *Agent) FixCacheOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.FixCache && a.FixCacheDir != "" && !a.Off["fixcache"]
}

// fixCacheBash records a bash check's result in the fix cache and returns
// the note for a red one whose error an earlier session fixed, or "".
// output is the call's result before it was archived.
func (a *Agent) fixCacheBash(name string, input []byte, output string, isErr bool, emit func(Event)) string {
	if name != "bash" || isErr || !a.FixCacheOn() || a.DryRunOn() {
		return ""
	}
	root := a.displayRoot()
	cmd := shell.TrimWorkspaceCd(shell.CommandOf(string(input)), root)
	e := session.Event{Name: name, Arguments: string(input), Output: output}
	if !shell.IsCheck(cmd) || neverRan(e) || IsProbe(cmd) {
		return ""
	}
	st := &a.fixc
	st.mu.Lock()
	defer st.mu.Unlock()
	path := fixcache.PathFor(a.FixCacheDir, root)
	if !runFailed(e) {
		p, ok := st.pending[cmd]
		if !ok {
			return ""
		}
		delete(st.pending, cmd)
		fix, files := a.fixCacheDiff(root, p.after)
		if fix == "" {
			return ""
		}
		// A store that does not parse starts over rather than blocking
		// every later fix.
		store, _ := fixcache.Open(path)
		if store.Add(fixcache.Entry{Signatures: p.sigs, Command: cmd, Fix: fix, Files: files, Session: st.id(), When: time.Now().UTC()}) && store.Save() == nil {
			emit(Event{Kind: EvNotice, Text: "fix cache: remembered the change that cleared " + textfmt.Plural(len(p.sigs), "error signature")})
		}
		return ""
	}
	sigs := fixcache.Signatures(output, root)
	if len(sigs) == 0 {
		return ""
	}
	if st.pending == nil {
		st.pending = map[string]fixCachePending{}
	}
	if _, ok := st.pending[cmd]; !ok {
		st.pending[cmd] = fixCachePending{sigs: sigs, after: a.fixCacheLastChange()}
	}
	store, err := fixcache.Open(path)
	if err != nil {
		return ""
	}
	hit, shared, ok := store.Lookup(sigs, st.id())
	if !ok || st.told[shared] {
		return ""
	}
	if st.told == nil {
		st.told = map[string]bool{}
	}
	st.told[shared] = true
	emit(Event{Kind: EvNotice, Text: "fix cache: this error was fixed in an earlier session"})
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "fixcache", Output: shared})
	return fixCacheNote(hit, shared)
}

// fixCacheLastChange is the number of the session's latest change, 0 when
// there is none.
func (a *Agent) fixCacheLastChange() int {
	n := 0
	for _, c := range a.Changes() {
		n = max(n, c.Seq)
	}
	return n
}

// fixCacheDiff is the diff of every file changed after change number
// after, from its content before the first such change to its content on
// disk now, and the files it covers relative to root. A change since
// undone counts for nothing.
func (a *Agent) fixCacheDiff(root string, after int) (string, []string) {
	before := map[string][]byte{}
	var order []string
	for _, c := range a.Changes() {
		if c.Seq <= after || c.Undone {
			continue
		}
		if _, ok := before[c.Path]; ok {
			continue
		}
		before[c.Path] = c.Before
		order = append(order, c.Path)
	}
	var b strings.Builder
	var files []string
	for _, p := range order {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, p)
		}
		now, _ := os.ReadFile(abs)
		rel := p
		if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
		d := diff.Unified(rel, string(before[p]), string(now), 1)
		if d == "" {
			continue
		}
		files = append(files, rel)
		b.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), files
}

// fixCacheNote is what the model reads under a red run whose error an
// earlier session fixed.
func fixCacheNote(e fixcache.Entry, shared string) string {
	when := ""
	if !e.When.IsZero() {
		when = " on " + e.When.Format("2006-01-02")
	}
	return "\n\n[harness] This error was seen before in this repository" + when + " (`" + textfmt.FirstLine(shared, 160) +
		"`), and it went away after this change:\n```diff\n" + strings.TrimRight(e.Fix, "\n") +
		"\n```\nThe cause here may differ; read the code before reusing it."
}
