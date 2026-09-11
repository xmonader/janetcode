// Package feature is janetcode's catalogue of itself: every slash
// command, command-line flag, tool, configuration key and keybinding,
// with one line of what it does, one line of when it helps, and how to
// invoke it.
//
// The catalogue is built from the same tables the program runs on — the
// TUI's command table, the flag set, the tool registry, the Config
// struct — rather than written out by hand, so it cannot describe a
// janetcode that does not exist. Tests fail when a command or a flag has
// no entry.
//
// Nothing here costs the model anything per request. The catalogue lives
// in the binary, is built once at startup from data that is already in
// memory, and reaches the model only when it calls the introspect tool.
package feature

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Kind groups the catalogue by how a feature is reached.
type Kind string

const (
	KindCommand Kind = "command" // a /command in the terminal
	KindKey     Kind = "key"     // a keybinding
	KindFlag    Kind = "flag"    // a command-line flag
	KindTool    Kind = "tool"    // a tool the model can call
	KindConfig  Kind = "config"  // a key in config.json
	KindBehave  Kind = "harness" // something the harness does on its own
)

// Kinds lists the kinds in report order.
var Kinds = []Kind{KindCommand, KindKey, KindFlag, KindTool, KindConfig, KindBehave}

// Feature is one thing janetcode can do.
type Feature struct {
	// Name identifies the feature within its kind: "/undo", "-p",
	// "read_file", "compact_at", "ctrl+g".
	Name string
	Kind Kind
	// What it does, one line.
	What string
	// When it helps, one line. This is the half a user cannot guess and
	// the half that makes a hint worth showing.
	When string
	// Invoke is what to type. Empty means Name is what to type.
	Invoke string
	// Topics are extra words a search should match: synonyms and the
	// problem the feature solves, not a restatement of What.
	Topics []string
	// Manual names the manual page that covers it, without the .md.
	Manual string
}

// Invocation is what the user types to reach the feature.
func (f Feature) Invocation() string {
	if f.Invoke != "" {
		return f.Invoke
	}
	return f.Name
}

// Line renders the feature as one catalogue row.
func (f Feature) Line() string {
	s := fmt.Sprintf("%-22s %s", f.Invocation(), f.What)
	if f.When != "" {
		s += " — " + f.When
	}
	return s
}

// Catalog holds the features of one build. It is safe for concurrent
// use: the TUI registers from the update loop while --inspect may read
// it from another.
type Catalog struct {
	mu   sync.RWMutex
	list []Feature
	idx  map[string]int // kind+"\x00"+name → position in list
}

// New returns an empty catalogue.
func New() *Catalog { return &Catalog{idx: map[string]int{}} }

func key(kind Kind, name string) string { return string(kind) + "\x00" + name }

// Add registers a feature, replacing one of the same kind and name. A
// feature with no name is ignored so a caller may register whatever a
// subsystem reports without checking it first.
func (c *Catalog) Add(f Feature) {
	if f.Name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, ok := c.idx[key(f.Kind, f.Name)]; ok {
		c.list[i] = f
		return
	}
	c.idx[key(f.Kind, f.Name)] = len(c.list)
	c.list = append(c.list, f)
}

// AddAll registers many features.
func (c *Catalog) AddAll(fs []Feature) {
	for _, f := range fs {
		c.Add(f)
	}
}

// Get looks a feature up exactly.
func (c *Catalog) Get(kind Kind, name string) (Feature, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	i, ok := c.idx[key(kind, name)]
	if !ok {
		return Feature{}, false
	}
	return c.list[i], true
}

// Len is how many features are registered.
func (c *Catalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.list)
}

// All returns every feature, sorted by kind then name.
func (c *Catalog) All() []Feature {
	c.mu.RLock()
	out := append([]Feature(nil), c.list...)
	c.mu.RUnlock()
	rank := map[Kind]int{}
	for i, k := range Kinds {
		rank[k] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := rank[out[i].Kind], rank[out[j].Kind]; a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// OfKind returns the features of one kind, sorted by name.
func (c *Catalog) OfKind(k Kind) []Feature {
	var out []Feature
	for _, f := range c.All() {
		if f.Kind == k {
			out = append(out, f)
		}
	}
	return out
}

// Find ranks the catalogue against a query and returns the best matches,
// most relevant first, at most limit of them. An empty query matches
// nothing: a search with no words is a listing, and List does that.
func (c *Catalog) Find(query string, limit int) []Feature {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.TrimPrefix(q, "/")
	if q == "" {
		return nil
	}
	words := strings.Fields(q)
	type scored struct {
		f Feature
		s int
	}
	var hits []scored
	for _, f := range c.All() {
		if s := score(f, q, words); s > 0 {
			hits = append(hits, scored{f, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Feature, len(hits))
	for i, h := range hits {
		out[i] = h.f
	}
	return out
}

// score rates one feature against a query. The name is worth far more
// than the prose: "undo" should find /undo before it finds the three
// other entries whose description mentions undoing.
func score(f Feature, q string, words []string) int {
	name := strings.ToLower(strings.TrimPrefix(f.Name, "/"))
	inv := strings.ToLower(f.Invocation())
	s := 0
	switch {
	case name == q:
		s += 1000
	case strings.HasPrefix(name, q):
		s += 600
	case strings.Contains(name, q), strings.Contains(inv, q):
		s += 300
	case subsequence(q, name):
		// /rsm finds /resume, the same rule the command menu uses.
		s += 150
	}
	hay := strings.ToLower(f.What + " " + f.When + " " + strings.Join(f.Topics, " "))
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		for _, t := range f.Topics {
			if strings.EqualFold(t, w) {
				s += 120
			}
		}
		if containsWord(hay, w) {
			s += 60
		}
	}
	return s
}

// containsWord reports whether hay contains w as a whole word or as a
// word with a short inflection on the end. "session" has to find
// "sessions" and "revert" has to find "reverted", but "undo" must not
// find "undocumented" — so the match has to start a word and leave at
// most a suffix behind.
const maxInflection = 3

func containsWord(hay, w string) bool {
	for i := 0; i+len(w) <= len(hay); {
		j := strings.Index(hay[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if j == 0 || !isWordByte(hay[j-1]) {
			tail := 0
			for k := j + len(w); k < len(hay) && isWordByte(hay[k]); k++ {
				tail++
			}
			if tail <= maxInflection {
				return true
			}
		}
		i = j + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// subsequence reports whether every byte of q appears in s in order.
func subsequence(q, s string) bool {
	if q == "" {
		return true
	}
	i := 0
	for j := 0; j < len(s) && i < len(q); j++ {
		if s[j] == q[i] {
			i++
		}
	}
	return i == len(q)
}

// List renders the whole catalogue, grouped by kind.
func (c *Catalog) List() string {
	all := c.All()
	if len(all) == 0 {
		return "no features registered"
	}
	var b strings.Builder
	var kind Kind
	for _, f := range all {
		if f.Kind != kind {
			kind = f.Kind
			fmt.Fprintf(&b, "\n%s (%d)\n", plural(kind), countKind(all, kind))
		}
		fmt.Fprintf(&b, "  %s\n", f.Line())
	}
	return strings.TrimSpace(b.String())
}

func countKind(all []Feature, k Kind) int {
	n := 0
	for _, f := range all {
		if f.Kind == k {
			n++
		}
	}
	return n
}

func plural(k Kind) string {
	switch k {
	case KindBehave:
		return "what the harness does on its own"
	case KindConfig:
		return "config keys"
	default:
		return string(k) + "s"
	}
}

// Summary is the one-line count used by --inspect features and /stats.
func (c *Catalog) Summary() string {
	all := c.All()
	var parts []string
	for _, k := range Kinds {
		if n := countKind(all, k); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(k)))
		}
	}
	return strings.Join(parts, ", ")
}

// Explain renders one feature in full, for /help <topic>.
func (f Feature) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  (%s)\n%s\n", f.Invocation(), f.Kind, f.What)
	if f.When != "" {
		fmt.Fprintf(&b, "when it helps: %s\n", f.When)
	}
	if f.Manual != "" {
		fmt.Fprintf(&b, "manual: docs/manual/%s.md\n", f.Manual)
	}
	return strings.TrimRight(b.String(), "\n")
}
