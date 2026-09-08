// Package tool defines the tools an agent can invoke on the local machine and
// a registry that exposes them to the model.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
)

// Tool is one capability the model can call.
type Tool interface {
	Name() string
	Description() string
	// Schema is the JSON Schema for the tool's input object.
	Schema() json.RawMessage
	// Run executes the tool. A returned error is reported to the model as an
	// error result, not as a crash.
	Run(ctx context.Context, input json.RawMessage) (string, error)
	// Mutating reports whether the tool changes the filesystem or runs
	// arbitrary commands, i.e. whether it should require permission.
	Mutating() bool
}

// Registry holds the tools available to an agent.
type Registry struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	deferred map[string]bool
}

// NewRegistry builds a registry from the given tools. Duplicate names panic
// because they are a programming error.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if _, dup := r.tools[t.Name()]; dup {
			panic("tool: duplicate tool name " + t.Name())
		}
		r.tools[t.Name()] = t
	}
	return r
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Add registers a tool at runtime (MCP servers come up after startup). An
// existing tool of the same name is replaced.
func (r *Registry) Add(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// AddDeferred registers a tool whose schema is not sent to the model until
// it is activated (by tool_search). This keeps big MCP tool sets from
// bloating every request.
func (r *Registry) AddDeferred(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
	if r.deferred == nil {
		r.deferred = map[string]bool{}
	}
	r.deferred[t.Name()] = true
}

// Activate makes a deferred tool's schema part of every request from now
// on. It reports whether the name was a deferred tool.
func (r *Registry) Activate(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.deferred[name] {
		return false
	}
	delete(r.deferred, name)
	return true
}

// Deferred returns the definitions of tools that are registered but not
// yet exposed, sorted by name.
func (r *Registry) Deferred() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.deferred))
	for n := range r.deferred {
		names = append(names, n)
	}
	sort.Strings(names)
	defs := make([]llm.ToolDef, 0, len(names))
	for _, n := range names {
		t := r.tools[n]
		defs = append(defs, llm.ToolDef{Name: n, Description: t.Description(), InputSchema: t.Schema()})
	}
	return defs
}

// Remove unregisters a tool.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.deferred, name)
}

// Defs returns the tool definitions in stable (sorted) order so the request
// prefix stays cacheable.
func (r *Registry) Defs() []llm.ToolDef { return r.DefsIf(nil) }

// DefsIf is Defs restricted to tools the predicate keeps; nil keeps all.
func (r *Registry) DefsIf(keep func(Tool) bool) []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		if !r.deferred[n] && (keep == nil || keep(r.tools[n])) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	defs := make([]llm.ToolDef, 0, len(names))
	for _, n := range names {
		t := r.tools[n]
		defs = append(defs, llm.ToolDef{Name: n, Description: t.Description(), InputSchema: t.Schema()})
	}
	return defs
}

// MaxOutput is the hard cap on what a tool returns, in bytes. The agent
// archives anything over its inline limit, so this only bounds memory.
const MaxOutput = 256 * 1024

// Truncate clips s to about MaxOutput bytes, keeping the head and the tail
// with a marker in between: the failure in a long test run is at the end.
func Truncate(s string) string {
	return TruncateTo(s, MaxOutput)
}

// TruncateTo is Truncate with an explicit byte budget.
func TruncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head, tail := max*2/3, max/3
	// Cut on line boundaries where possible so no line is left half-shown.
	if i := strings.LastIndexByte(s[:head], '\n'); i > head/2 {
		head = i
	}
	if i := strings.IndexByte(s[len(s)-tail:], '\n'); i >= 0 && i < tail/2 {
		tail -= i + 1
	}
	return s[:head] + fmt.Sprintf("\n... [%d bytes omitted; the output continues] ...\n", len(s)-head-tail) + s[len(s)-tail:]
}

// resolvePath makes p absolute relative to root and rejects escapes outside
// root. Symlinks inside root that point outside are also rejected.
func resolvePath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", p)
	}
	// Resolve symlinks on the deepest existing ancestor.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		realRoot, rerr := filepath.EvalSymlinks(root)
		if rerr != nil {
			realRoot = root
		}
		rel2, err := filepath.Rel(realRoot, real)
		if err != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q resolves outside the working directory", p)
		}
	}
	return abs, nil
}

func decode(input json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Previewer is implemented by tools that can describe what Run would do
// without doing it, for permission prompts. An empty string means there is
// nothing useful to show beyond the input itself.
type Previewer interface {
	Preview(ctx context.Context, input json.RawMessage) (string, error)
}

// Toucher is implemented by tools that modify files at known paths, so the
// agent can snapshot them before the call and offer an undo.
type Toucher interface {
	Touches(input json.RawMessage) ([]string, error)
}

// Guard is implemented by tools whose inputs can be risky enough that the
// user must be asked even when auto-approve is on or the tool was allowed
// for the session. The reason is shown in the prompt.
type Guard interface {
	Guard(input json.RawMessage) (ask bool, reason string)
}

// Warner is implemented by tools that can flag risky inputs so the
// permission prompt can call them out. Empty means nothing to flag.
type Warner interface {
	Warn(input json.RawMessage) string
}
