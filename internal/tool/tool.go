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
	tools map[string]Tool
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
	t, ok := r.tools[name]
	return t, ok
}

// Defs returns the tool definitions in stable (sorted) order so the request
// prefix stays cacheable.
func (r *Registry) Defs() []llm.ToolDef {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
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

// MaxOutput caps tool output sent back to the model, in bytes.
const MaxOutput = 64 * 1024

// Truncate clips s to MaxOutput bytes with a marker so the model knows.
func Truncate(s string) string {
	if len(s) <= MaxOutput {
		return s
	}
	return s[:MaxOutput] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-MaxOutput)
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

// Warner is implemented by tools that can flag risky inputs so the
// permission prompt can call them out. Empty means nothing to flag.
type Warner interface {
	Warn(input json.RawMessage) string
}
