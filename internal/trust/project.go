package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A project is trusted as a whole, at a level, as well as file by file.
// The per-file approvals above (/hooks trust, /verify trust, /mcp trust,
// /permissions trust) predate the levels and still work for a project
// nobody has decided about; once a level is recorded it is the answer,
// so "untrusted" switches off a hook approved last month as well.
//
// The level is recorded against a fingerprint of every capability file
// the project ships, so editing any of them — a new skill, a changed
// hook — undoes the decision and asks again.

// Level is how far a project's own capability files are let in.
type Level int

const (
	// Undecided: nothing is recorded. Instruction files stay out; the
	// executable ones follow their own per-file approvals, as they
	// always did.
	Undecided Level = iota
	// Untrusted: none of the project's capability files is used.
	Untrusted
	// Instructions: the text-only ones (skills, agent profiles, custom
	// commands, path-scoped rules) are read; nothing the project ships
	// runs.
	Instructions
	// Full: everything, including hooks, MCP servers, verify commands,
	// allow rules and recipes. It never approves a tool call by itself:
	// the permission prompts are untouched.
	Full
)

func (l Level) String() string {
	switch l {
	case Untrusted:
		return "untrusted"
	case Instructions:
		return "instructions"
	case Full:
		return "full"
	}
	return "undecided"
}

// ParseLevel reads a level as a user types it.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "untrusted", "none", "no", "off", "revoke":
		return Untrusted, nil
	case "instructions", "instr", "text", "read":
		return Instructions, nil
	case "full", "all", "yes", "trust":
		return Full, nil
	}
	return Undecided, fmt.Errorf("unknown trust level %q: want untrusted, instructions or full", s)
}

// ProjectKey is the store key for a project's level.
func ProjectKey(cwd string) string { return cwd + "#project" }

// Capability is one project-supplied file (or directory of files) that
// the levels govern.
type Capability struct {
	Kind string // "skills", "hooks", …
	Path string // relative to the project directory, slash-separated
	// Exec marks something that runs code or reaches out (hooks, MCP,
	// verify, allow rules, recipes) rather than text the model reads.
	Exec bool
}

// capabilitySpec lists where each kind lives, relative to the project.
var capabilitySpec = []struct {
	kind string
	path string
	exec bool
	dir  bool
}{
	{"skills", ".janetcode/skills", false, true},
	{"skills", ".agents/skills", false, true},
	{"skills", ".claude/skills", false, true},
	{"agent profiles", ".janetcode/agents", false, true},
	{"agent profiles", ".agents/agents", false, true},
	{"agent profiles", ".claude/agents", false, true},
	{"custom commands", ".janetcode/commands", false, true},
	{"rules", ".janetcode/rules", false, true},
	{"rules", ".claude/rules", false, true},
	{"hooks", ".janetcode/hooks.json", true, false},
	{"MCP servers", ".mcp.json", true, false},
	{"verify commands", ".janetcode/verify.json", true, false},
	{"allow rules", ".janetcode/permissions.json", true, false},
	{"recipes", ".janetcode/recipes", true, true},
}

// Capabilities lists the capability files present in the project at dir.
// .agents/<name>.md profiles are covered by the .agents directory as a
// whole, which is why it is walked for .md files only at its top.
func Capabilities(dir string) []Capability {
	if dir == "" {
		return nil
	}
	var out []Capability
	for _, c := range capabilitySpec {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(c.path)))
		if err != nil || fi.IsDir() != c.dir {
			continue
		}
		if c.dir && len(listFiles(filepath.Join(dir, filepath.FromSlash(c.path)))) == 0 {
			continue
		}
		out = append(out, Capability{Kind: c.kind, Path: c.path, Exec: c.exec})
	}
	if entries, err := os.ReadDir(filepath.Join(dir, ".agents")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				out = append(out, Capability{Kind: "agent profiles", Path: ".agents/" + e.Name()})
			}
		}
	}
	return out
}

// maxFingerprintFiles bounds how much one fingerprint reads: a skills
// directory with a vendored tree in it should cost a bounded read, not
// the whole tree on every check.
const (
	maxFingerprintFiles = 400
	maxFingerprintBytes = 8 << 20
)

func listFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(out) >= maxFingerprintFiles {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// Fingerprint hashes every capability file in the project at dir: names
// and contents. "" when the project ships none, which is also what makes
// a level recorded for an empty project never match anything.
func Fingerprint(dir string) string {
	caps := Capabilities(dir)
	if len(caps) == 0 {
		return ""
	}
	var files []string
	for _, c := range caps {
		p := filepath.Join(dir, filepath.FromSlash(c.Path))
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			files = append(files, listFiles(p)...)
		} else {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	h := sha256.New()
	read := 0
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		if read >= maxFingerprintBytes {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		read += len(data)
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecordedLevel returns the level stored for dir and the fingerprint it
// was recorded against.
func (t Store) RecordedLevel(dir string) (Level, string) {
	if t.Path == "" {
		return Undecided, ""
	}
	m, err := t.load()
	if err != nil {
		return Undecided, ""
	}
	v, ok := m[ProjectKey(dir)]
	if !ok {
		return Undecided, ""
	}
	name, hash, _ := strings.Cut(v, ":")
	lvl, err := ParseLevel(name)
	if err != nil {
		return Undecided, ""
	}
	return lvl, hash
}

// SetLevel records the project's level against its current fingerprint.
func (t Store) SetLevel(dir string, lvl Level, hash string) error {
	return t.Trust(ProjectKey(dir), lvl.String()+":"+hash)
}

// Forget drops a key from the store.
func (t Store) Forget(key string) error {
	if t.Path == "" {
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	m, err := t.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return t.writeMap(m)
}

// Gate answers, for a project directory, how far its capability files are
// let in. It is shared by everything that loads one — the skill loader
// asks it on every turn — so it is safe for concurrent use and caches the
// fingerprint for a moment rather than rehashing the skills directory on
// every model call.
//
// A nil *Gate keeps the behaviour from before the levels existed: the
// instruction-class files are read and each executable file follows its
// own approval. Code built without one (a test, a subagent) is unchanged.
type Gate struct {
	Store Store
	// Flag is a level given for this run (--trust, JANETCODE_TRUST);
	// it wins over everything and is never written down.
	Flag Level

	mu      sync.Mutex
	session map[string]Level
	cache   map[string]gateEntry
	now     func() time.Time
}

type gateEntry struct {
	at   time.Time
	gen  int
	hash string
}

// cacheFor is how long a fingerprint is reused.
const cacheFor = 2 * time.Second

// State is the answer for one directory.
type State struct {
	Dir   string
	Level Level
	// Source says where Level came from: "flag", "session", "stored",
	// "changed" (a level was stored but the files changed since) or
	// "none".
	Source string
	// Decided is false for a project nobody has chosen a level for (or
	// whose files changed since), where the executable files fall back
	// to their own per-file approvals.
	Decided bool
	Hash    string
	Caps    []Capability
}

func (g *Gate) fingerprint(dir string) string {
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	g.mu.Lock()
	gen := selfGeneration()
	if e, ok := g.cache[dir]; ok && e.gen == gen && now().Sub(e.at) < cacheFor {
		g.mu.Unlock()
		return e.hash
	}
	g.mu.Unlock()
	h := Fingerprint(dir)
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]gateEntry{}
	}
	g.cache[dir] = gateEntry{at: now(), gen: gen, hash: h}
	g.mu.Unlock()
	return h
}

// Invalidate drops the cached fingerprints, after a write the gate should
// see at once.
func (g *Gate) Invalidate() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.cache = nil
	g.mu.Unlock()
}

// State works out the level for the project at dir.
func (g *Gate) State(dir string) State {
	st := State{Dir: dir, Level: Full, Source: "none", Decided: true}
	if g == nil {
		return st
	}
	st.Hash = g.fingerprint(dir)
	st.Caps = Capabilities(dir)
	st.Level, st.Decided = Undecided, false
	if g.Flag != Undecided {
		st.Level, st.Source, st.Decided = g.Flag, "flag", true
		return st
	}
	g.mu.Lock()
	lvl, ok := g.session[dir]
	g.mu.Unlock()
	if ok {
		st.Level, st.Source, st.Decided = lvl, "session", true
		return st
	}
	rec, hash := g.Store.RecordedLevel(dir)
	switch {
	case rec == Undecided:
	case rec == Untrusted:
		// Distrust needs no fingerprint: a changed file is no reason
		// to start trusting it.
		st.Level, st.Source, st.Decided = Untrusted, "stored", true
	case hash == st.Hash && hash != "":
		st.Level, st.Source, st.Decided = rec, "stored", true
	default:
		st.Source = "changed"
		if hash != "" && takeSelfWrite(dir) {
			// janetcode itself wrote the change (/verify set, a saved
			// recipe, /mcp add) on the user's say-so: the level they
			// chose carries over to the new contents.
			if err := g.Store.SetLevel(dir, rec, st.Hash); err == nil {
				st.Level, st.Source, st.Decided = rec, "stored", true
			}
		}
	}
	return st
}

var (
	selfMu     sync.Mutex
	selfWrites = map[string]bool{}
	selfGen    int
)

// SelfWrote records that janetcode itself just wrote one of the project's
// capability files at the user's request, so the change does not knock
// the project's recorded level back to "choose again". Writers call it
// after the file is on disk.
func SelfWrote(dir string) {
	selfMu.Lock()
	selfWrites[dir] = true
	selfGen++
	selfMu.Unlock()
}

func takeSelfWrite(dir string) bool {
	selfMu.Lock()
	defer selfMu.Unlock()
	ok := selfWrites[dir]
	delete(selfWrites, dir)
	return ok
}

func selfGeneration() int {
	selfMu.Lock()
	defer selfMu.Unlock()
	return selfGen
}

// SetSession sets the level for dir until the process exits.
func (g *Gate) SetSession(dir string, lvl Level) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session == nil {
		g.session = map[string]Level{}
	}
	g.session[dir] = lvl
}

// Set records lvl for dir, for good or for this session only. A recorded
// level also clears this process's session override for dir.
func (g *Gate) Set(dir string, lvl Level, persistent bool) error {
	if g == nil {
		return fmt.Errorf("project trust is not available here")
	}
	g.Invalidate()
	if !persistent {
		g.SetSession(dir, lvl)
		return nil
	}
	if err := g.Store.SetLevel(dir, lvl, Fingerprint(dir)); err != nil {
		return err
	}
	g.mu.Lock()
	delete(g.session, dir)
	g.mu.Unlock()
	if lvl == Untrusted {
		// Revoking also revokes the per-file approvals, so going back
		// to undecided later does not quietly bring them back.
		for _, k := range []string{dir, HooksKey(dir), VerifyKey(dir), PermissionsKey(dir)} {
			if err := g.Store.Forget(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// Instructions reports whether the project's text-only capability files
// (skills, agent profiles, custom commands, rules) may be read.
func (g *Gate) Instructions(dir string) bool {
	st := g.State(dir)
	return st.Level >= Instructions
}

// Exec reports whether one of the project's executable files may be used,
// given whether that file has its own per-file approval. A decided level
// is the answer on its own; an undecided project falls back to the
// approval, which is how every project worked before the levels.
func (g *Gate) Exec(dir string, approved bool) bool {
	if g == nil {
		return approved
	}
	st := g.State(dir)
	if st.Decided {
		return st.Level == Full
	}
	return approved
}

// ProjectDir is dir when the project's instruction-class files may be
// read and "" otherwise — the shape the loaders take ("" skips the
// project's locations and keeps the user's own).
func (g *Gate) ProjectDir(dir string) string {
	if g.Instructions(dir) {
		return dir
	}
	return ""
}

// NeedsDecision reports a project that ships capability files and has no
// level the user chose for its current contents: the case a front-end
// asks about on first run.
func (g *Gate) NeedsDecision(dir string) bool {
	if g == nil {
		return false
	}
	st := g.State(dir)
	return !st.Decided && len(st.Caps) > 0
}

// Report describes the level and every capability it governs, the way
// /trust and --inspect trust print it.
func (st State) Report() string {
	var sb strings.Builder
	lvl := st.Level
	fmt.Fprintf(&sb, "project trust: %s", lvl)
	switch st.Source {
	case "flag":
		sb.WriteString(" (this run, from --trust)")
	case "session":
		sb.WriteString(" (this session only)")
	case "stored":
		sb.WriteString(" (recorded)")
	case "changed":
		sb.WriteString(" (a level was recorded, but the project's files changed since; choose again)")
	default:
		if len(st.Caps) > 0 {
			sb.WriteString(" (not chosen yet)")
		}
	}
	sb.WriteString("\n")
	if len(st.Caps) == 0 {
		sb.WriteString("this project ships no skills, agents, commands, rules, hooks, MCP servers, verify commands, allow rules or recipes\n")
		return sb.String()
	}
	for _, c := range st.Caps {
		on := "off"
		switch {
		case !c.Exec && lvl >= Instructions:
			on = "on"
		case c.Exec && lvl == Full:
			on = "on"
		case c.Exec && !st.Decided:
			on = "per-file approval"
		}
		fmt.Fprintf(&sb, "  %-16s %-30s %s\n", c.Kind, c.Path, on)
	}
	sb.WriteString("levels: untrusted (nothing) · instructions (skills, agents, commands, rules) · full (also hooks, MCP, verify, allow rules, recipes)\n")
	sb.WriteString("trust never approves a tool call: the permission prompts are unchanged\n")
	return sb.String()
}
