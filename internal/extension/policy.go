// Package extension is the one place that knows what every extension of
// a session is allowed to do: the MCP servers, hooks and skills that
// run code or feed the model text, where each came from, why it is or is
// not trusted, and which capabilities of it the user has switched on.
//
// Trust is staged. A server the user wrote in their own config file is
// trusted by authorship. A server in a project's .mcp.json is trusted
// once the file is approved. A server installed with /mcp add starts
// restricted: it can connect and be read from, and nothing it offers
// that may change things is callable until /mcp enable <name> mutate.
// Disabling a server stops its process and refuses every later call.
package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// Scope is one capability of an extension that can be granted alone.
type Scope string

const (
	// Connect starts the server's process, or opens the connection to a
	// remote one. Without it nothing of the server runs or is contacted.
	Connect Scope = "connect"
	// Read lets the model call the tools the server marks read-only, and
	// list and read its resources and prompts.
	Read Scope = "read"
	// Mutate lets the model call every other tool of the server: one that
	// is not marked read-only may write files, run commands or reach
	// the network, and the harness cannot tell which.
	Mutate Scope = "mutate"
)

// AllScopes lists the scopes in the order they are reported.
var AllScopes = []Scope{Connect, Read, Mutate}

// ParseScope accepts a scope name.
func ParseScope(s string) (Scope, error) {
	switch sc := Scope(strings.ToLower(strings.TrimSpace(s))); sc {
	case Connect, Read, Mutate:
		return sc, nil
	}
	return "", fmt.Errorf("unknown capability %q: want connect, read or mutate", s)
}

// Grant is what the user decided about one extension.
type Grant struct {
	Scopes []Scope `json:"scopes"`
	// DisabledTools are server tool names (without the mcp__ prefix)
	// switched off even though their scope is granted.
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// Reason says how the grant came about, for the report.
	Reason  string    `json:"reason,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
}

// Has reports whether the grant includes the scope.
func (g Grant) Has(s Scope) bool {
	for _, x := range g.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// ToolDisabled reports whether one tool was switched off by name.
func (g Grant) ToolDisabled(tool string) bool {
	for _, t := range g.DisabledTools {
		if t == tool {
			return true
		}
	}
	return false
}

// With returns the grant with scopes added; Connect comes with Read and
// Mutate, since neither means anything on a server that is not running.
// Mutate comes with Read: a grant to change things that cannot be
// inspected is not one anybody means.
func (g Grant) With(scopes ...Scope) Grant {
	set := scopeSet(g.Scopes)
	for _, s := range scopes {
		set[s] = true
		if s == Read || s == Mutate {
			set[Connect] = true
		}
		if s == Mutate {
			set[Read] = true
		}
	}
	g.Scopes = ordered(set)
	return g
}

// Without returns the grant with scopes removed. Taking Connect away
// takes everything, because a server that cannot run cannot be read;
// taking Read away takes Mutate with it for the same reason With ties
// them together.
func (g Grant) Without(scopes ...Scope) Grant {
	set := scopeSet(g.Scopes)
	for _, s := range scopes {
		delete(set, s)
		switch s {
		case Connect:
			set = map[Scope]bool{}
		case Read:
			delete(set, Mutate)
		}
	}
	g.Scopes = ordered(set)
	return g
}

func scopeSet(list []Scope) map[Scope]bool {
	set := map[Scope]bool{}
	for _, s := range list {
		set[s] = true
	}
	return set
}

func ordered(set map[Scope]bool) []Scope {
	out := []Scope{}
	for _, s := range AllScopes {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// Restricted is what a newly installed server gets: it can run and be
// read from, and nothing that may change things is callable.
func Restricted(reason string) Grant {
	return Grant{Scopes: []Scope{Connect, Read}, Reason: reason, Updated: time.Now().UTC()}
}

// Full is every capability.
func Full(reason string) Grant {
	return Grant{Scopes: append([]Scope(nil), AllScopes...), Reason: reason, Updated: time.Now().UTC()}
}

// maxStoreBytes bounds the file we are willing to parse, so a hostile
// file costs a refusal and not the heap.
const maxStoreBytes = 1 << 20

var writeMu sync.Mutex

// Store keeps the grants of one user, in one file beside the config.
//
// Reads fail closed: a store that cannot be read is an error the policy
// turns into a refusal, never into "no restrictions recorded".
type Store struct{ Path string }

// UserKey is the key of a server declared in the user's own config.
func UserKey(name string) string { return "user:" + name }

// ProjectKey is the key of a server from a project's .mcp.json.
func ProjectKey(cwd, name string) string { return "project:" + cwd + ":" + name }

func (s Store) load() (map[string]Grant, error) {
	m := map[string]Grant{}
	if s.Path == "" {
		return m, nil
	}
	info, err := os.Stat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > maxStoreBytes {
		return nil, fmt.Errorf("%s: %d bytes, over the %d limit", s.Path, info.Size(), maxStoreBytes)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	if m == nil {
		m = map[string]Grant{}
	}
	return m, nil
}

// Get returns the recorded grant for key, if any.
func (s Store) Get(key string) (Grant, bool, error) {
	m, err := s.load()
	if err != nil {
		return Grant{}, false, err
	}
	g, ok := m[key]
	return g, ok, nil
}

// Set records a grant.
func (s Store) Set(key string, g Grant) error {
	return s.update(func(m map[string]Grant) { m[key] = g })
}

// Delete forgets a grant, as when the extension is removed.
func (s Store) Delete(key string) error {
	if s.Path == "" {
		return nil
	}
	return s.update(func(m map[string]Grant) { delete(m, key) })
}

func (s Store) update(fn func(map[string]Grant)) error {
	if s.Path == "" {
		return errors.New("no extension store path")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	fn(m)
	for k, g := range m {
		sort.Strings(g.DisabledTools)
		m[k] = g
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.Path, append(data, '\n'), 0o600)
}

// FileName is the store's name inside the config directory.
const FileName = "extensions.json"

// StorePath is where the store lives for a config directory.
func StorePath(cfgDir string) string { return filepath.Join(cfgDir, FileName) }

// Policy answers, for a running session, what an MCP server may do. It
// implements mcptool's Policy: Connect is asked before a server starts
// and Call before every tool, resource or prompt use.
//
// A server with no recorded grant is as the gate that started it left
// it: the user's own config, or a project file the user approved. That
// is what keeps every setup that existed before this store working. A
// recorded grant always wins.
type Policy struct {
	Store Store
	// Cwd is the project directory; a project server's grant is keyed by
	// it, so the same name in another project is another grant.
	Cwd string
}

// Lookup finds the grant that governs name: the project's entry first,
// since a project server shadows a global one of the same name.
func (p Policy) Lookup(name string) (g Grant, key string, found bool, err error) {
	for _, k := range []string{ProjectKey(p.Cwd, name), UserKey(name)} {
		g, ok, err := p.Store.Get(k)
		if err != nil {
			return Grant{}, k, false, err
		}
		if ok {
			return g, k, true, nil
		}
	}
	return Grant{}, "", false, nil
}

// CheckConnect refuses to start a server whose connect scope was taken
// away, or whose grant cannot be read.
func (p Policy) CheckConnect(name string) error {
	g, _, found, err := p.Lookup(name)
	switch {
	case err != nil:
		return fmt.Errorf("extension store unreadable, so %s is not started: %w", name, err)
	case found && !g.Has(Connect):
		return fmt.Errorf("%s is disabled (/mcp enable %s turns it back on)", name, name)
	}
	return nil
}

// CheckCall refuses a use of a server's tool that its grant does not
// cover. mutating is the tool's own answer: true unless the server said
// read-only. A resource or prompt use passes mutating false and tool "".
func (p Policy) CheckCall(server, tool string, mutating bool) error {
	g, _, found, err := p.Lookup(server)
	switch {
	case err != nil:
		return fmt.Errorf("extension store unreadable, so %s is refused: %w", server, err)
	case !found:
		return nil
	case !g.Has(Connect):
		return fmt.Errorf("%s is disabled; nothing of it can be called (/mcp enable %s)", server, server)
	case tool != "" && g.ToolDisabled(tool):
		return fmt.Errorf("%s.%s is switched off (/mcp tool %s %s on)", server, tool, server, tool)
	case mutating && !g.Has(Mutate):
		return fmt.Errorf("%s is restricted: %q is not marked read-only, so it may change things and is blocked until /mcp enable %s mutate", server, tool, server)
	case !mutating && !g.Has(Read):
		return fmt.Errorf("%s is not allowed to be read from (/mcp enable %s read)", server, server)
	}
	return nil
}
