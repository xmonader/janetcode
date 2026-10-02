// Package tool defines the tools an agent can invoke on the local machine and
// a registry that exposes them to the model.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Result is what a tool may return besides text: screenshots a vision
// model should see. Tools that only implement Run still work; Rich is
// optional.
type Result struct {
	Text   string
	Images []llm.Block
}

// Rich is a tool that can return images with its text.
type Rich interface {
	RunRich(ctx context.Context, input json.RawMessage) (Result, error)
}

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
	// withheld names a tool that policy removed, and why. Distinct from
	// a typo: "unknown" invites a hunt, "withheld" ends one.
	withheld map[string]string
	// disabled, when set, names tools that may never be registered (see
	// Disable), and disabledWhy is what Withheld then says about them.
	disabled    func(string) bool
	disabledWhy string
	// Intent adds a short "why this call" field to every tool's schema.
	// It is set on the registry rather than per tool so that MCP tools and
	// anything registered at runtime carry it too. Set it before the
	// registry is shared, or with SetIntent.
	Intent bool
	// version changes whenever the set of visible tools does. It exists
	// so a caller that only wants the size of the definitions — the
	// context gauge, which the TUI recomputes on every keystroke — can
	// cache that instead of asking seventeen tools to describe
	// themselves again (182µs and 434 allocations a time).
	version uint64
	// cached is the last result of Defs and the version it was built
	// from; see Defs.
	cached    []llm.ToolDef
	cachedVer uint64
}

// SetIntent switches the intent field on or off, safe while another
// goroutine builds tool definitions (/config intent during a turn).
func (r *Registry) SetIntent(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Intent = on
}

// Version identifies the current set of visible tool definitions. It
// changes when a tool is added, removed, deferred or activated, and when
// the intent field is switched on or off. Two calls returning the same
// value mean Defs would return the same bytes.
func (r *Registry) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.versionLocked()
}

// versionLocked is Version with the lock already held.
func (r *Registry) versionLocked() uint64 {
	v := r.version << 1
	if r.Intent {
		v |= 1
	}
	return v
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
	if r.refusedLocked(t.Name()) {
		return
	}
	r.tools[t.Name()] = t
	r.version++
}

// AddDeferred registers a tool whose schema is not sent to the model until
// it is activated (by tool_search). This keeps big MCP tool sets from
// bloating every request.
func (r *Registry) AddDeferred(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusedLocked(t.Name()) {
		return
	}
	r.tools[t.Name()] = t
	if r.deferred == nil {
		r.deferred = map[string]bool{}
	}
	r.deferred[t.Name()] = true
	r.version++
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
	r.version++
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

// Disable removes every registered tool disabled names, and refuses to
// register one later (an MCP server's tools come up after startup). Each
// is recorded as withheld with why, so a call to one is answered as a
// policy decision rather than a typo.
func (r *Registry) Disable(disabled func(string) bool, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disabled, r.disabledWhy = disabled, why
	for name := range r.tools {
		if disabled(name) {
			delete(r.tools, name)
			delete(r.deferred, name)
			if r.withheld == nil {
				r.withheld = map[string]string{}
			}
			r.withheld[name] = why
		}
	}
	r.version++
}

// refusedLocked reports whether Disable refuses name, recording it as
// withheld. Called with mu held.
func (r *Registry) refusedLocked(name string) bool {
	if r.disabled == nil || !r.disabled(name) {
		return false
	}
	if r.withheld == nil {
		r.withheld = map[string]string{}
	}
	r.withheld[name] = r.disabledWhy
	return true
}

// Remove unregisters a tool.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.deferred, name)
	r.version++
}

// Withhold records that name is absent because of policy, not a typo.
func (r *Registry) Withhold(name, reason string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.withheld == nil {
		r.withheld = map[string]string{}
	}
	r.withheld[name] = reason
}

// Withheld reports why a missing tool was removed, if it was.
func (r *Registry) Withheld(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.withheld[name]
	return s, ok
}

// Defs returns the tool definitions in stable (sorted) order so the request
// prefix stays cacheable.
//
// The result is memoised on Version: the definitions are rebuilt from the
// tools only when the visible set (or the intent field) has changed. Every
// request, every context gauge and every subagent build asks for them, and
// asking seventeen tools to describe themselves — and re-marshalling every
// schema through WithIntent — is not free.
func (r *Registry) Defs() []llm.ToolDef {
	v := r.Version()
	r.mu.RLock()
	if r.cached != nil && r.cachedVer == v {
		defs := r.cached
		r.mu.RUnlock()
		return append([]llm.ToolDef(nil), defs...)
	}
	r.mu.RUnlock()
	defs := r.DefsIf(nil)
	r.mu.Lock()
	// Only store what was built from the state this version describes: a
	// concurrent Add may have moved the version on while we were out of
	// the lock, and caching a stale list under a new version would serve
	// definitions that no longer match the registry.
	if r.versionLocked() == v {
		r.cached, r.cachedVer = defs, v
	}
	r.mu.Unlock()
	return append([]llm.ToolDef(nil), defs...)
}

// DefsIf is Defs restricted to tools the predicate keeps; nil keeps all.
func (r *Registry) DefsIf(keep func(Tool) bool) []llm.ToolDef {
	// The snapshot is taken under the lock and the descriptions are built
	// outside it: a tool may describe itself in terms of the others (a
	// script can call them), and that would otherwise re-enter the lock.
	r.mu.RLock()
	kept := make([]Tool, 0, len(r.tools))
	for n, t := range r.tools {
		if !r.deferred[n] && (keep == nil || keep(t)) {
			kept = append(kept, t)
		}
	}
	intent := r.Intent
	r.mu.RUnlock()
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name() < kept[j].Name() })
	defs := make([]llm.ToolDef, 0, len(kept))
	for _, t := range kept {
		schema := t.Schema()
		if intent {
			schema = WithIntent(schema)
		}
		defs = append(defs, llm.ToolDef{Name: t.Name(), Description: t.Description(), InputSchema: schema})
	}
	return defs
}

// Names lists the tools the model can see, in order. It does not ask them
// to describe themselves, so a tool may call it from its own description.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		if !r.deferred[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// resolvePath makes p absolute relative to root and rejects escapes outside
// root. Symlinks inside root that point outside are also rejected.
// displayPath is how a tool names a file in its result: relative to the
// working directory when it lies under it, so the model and the user read
// "main.go" rather than an absolute path that fills the line.
func displayPath(root, p string) string {
	if root == "" || !filepath.IsAbs(p) {
		return p
	}
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func resolvePath(root, p string) (string, error) {
	return confinePath(root, p, true)
}

// confineIn tries each root in order. Relative paths are first joined to
// the working directory (roots[0]); an extra root then admits that
// absolute path if it falls inside it.
func confineIn(roots []string, p string, evalSymlinks bool) (string, error) {
	if len(roots) == 0 || strings.TrimSpace(roots[0]) == "" {
		return confinePath("", p, evalSymlinks)
	}
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(roots[0], p)
	}
	abs = filepath.Clean(abs)
	var last error
	for _, root := range roots {
		got, err := confineAbs(root, abs, p, evalSymlinks)
		if err == nil {
			return got, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("path %q is outside the working directory", p)
	}
	return "", last
}

// confinePath makes p absolute relative to root and rejects escapes
// outside root. evalSymlinks is the local-filesystem check; a remote
// host skips it so a path is not resolved against this machine.
func confinePath(root, p string, evalSymlinks bool) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	return confineAbs(root, abs, p, evalSymlinks)
}

func confineAbs(root, abs, named string, evalSymlinks bool) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", named)
	}
	if !evalSymlinks {
		return abs, nil
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The path does not exist yet, which is the normal case for a
		// write or an mkdir. Its ancestors do, though, and one of them
		// may be a symlink out of the root: `root/link -> /etc` makes
		// `root/link/passwd` a lexically confined path that writes to
		// /etc. So the nearest ancestor that does resolve is resolved
		// and the missing tail put back on, and that is what the check
		// below judges.
		real, err = resolveExisting(abs)
		if err != nil {
			return abs, nil
		}
	}
	realRoot, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		realRoot = root
	}
	rel2, err := filepath.Rel(realRoot, real)
	if err != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the working directory", named)
	}
	return abs, nil
}

// resolveExisting resolves the symlinks of the deepest ancestor of abs
// that exists and puts the missing tail back on, so a path that is about
// to be created is judged by where it would actually land.
func resolveExisting(abs string) (string, error) {
	rest := ""
	dir := abs
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no resolvable ancestor of %q", abs)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest), nil
		}
	}
}

func decode(input json.RawMessage, v any) error {
	err := decodeStrict(input, v)
	if err != nil {
		// Models often send a list or an object as a string holding its
		// JSON ("paths":"[\"a.go\"]"). That is unambiguous, and
		// rejecting it costs a round trip, so it is unwrapped and tried
		// once more. If that fails too, its error is the one reported: it
		// is about what is still wrong.
		if fixed, ok := unquoteStructured(input, v); ok {
			return decodeStrict(fixed, v)
		}
	}
	return err
}

func decodeStrict(input json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// A rejected field costs the model a whole round trip, and the
		// message it read said only which name was wrong, never which
		// names are right: a recorded session lost a step guessing
		// "timeout" for "timeout_ms". The accepted names are in the
		// struct; say them.
		if fields := jsonFields(v); fields != "" && strings.Contains(err.Error(), "unknown field") {
			// The closest accepted name first: "timeout" for "timeout_ms"
			// is a near miss, and saying so saves reading the list.
			if bad := unknownFieldName(err); bad != "" {
				if near := textfmt.NearestName(bad, strings.Split(fields, ", ")); near != "" {
					return fmt.Errorf("invalid input: %w; did you mean %q? This tool accepts: %s", err, near, fields)
				}
			}
			return fmt.Errorf("invalid input: %w; this tool accepts: %s", err, fields)
		}
		// encoding/json says "cannot unmarshal string into Go struct field
		// .limit of type int": Go's words for a JSON shape. Say it in JSON's.
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return fmt.Errorf("invalid input: %s must be %s, not %s", te.Field, jsonShape(te.Type), jsonKind(te.Value))
		}
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

// unknownFieldName is the name in encoding/json's `json: unknown field
// "x"` error, or "".
func unknownFieldName(err error) string {
	msg := err.Error()
	i := strings.Index(msg, `unknown field "`)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(`unknown field "`):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// jsonShape names the JSON value a Go type decodes from.
func jsonShape(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.String {
			return "a list of strings"
		}
		return "a list"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return "a " + t.String()
}

// jsonKind names what encoding/json found ("string", "number 1.5",
// "array") in plain words.
func jsonKind(v string) string {
	switch {
	case v == "array":
		return "a list"
	case v == "object":
		return "an object"
	case v == "bool":
		return "true/false"
	case strings.HasPrefix(v, "number"):
		return "a number (" + strings.TrimSpace(strings.TrimPrefix(v, "number")) + ")"
	case v == "":
		return "that value"
	}
	return "a " + v
}

// unquoteStructured replaces each top-level value of input that is a
// string holding a JSON array or object with that array or object, when
// the field it lands in is a list, a map or a struct. It reports whether
// it changed anything.
func unquoteStructured(input json.RawMessage, v any) (json.RawMessage, bool) {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return nil, false
	}
	changed := false
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		raw, ok := obj[name]
		if !ok || !structured(f.Type) || len(raw) == 0 || raw[0] != '"' {
			continue
		}
		var inner string
		if json.Unmarshal(raw, &inner) != nil {
			continue
		}
		inner = strings.TrimSpace(inner)
		if inner == "" || (inner[0] != '[' && inner[0] != '{') || !json.Valid([]byte(inner)) {
			continue
		}
		obj[name] = json.RawMessage(inner)
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(obj)
	return out, err == nil
}

// structured reports whether a field decodes from a JSON array or object
// only. A byte slice (json.RawMessage among them) is excluded: it takes
// any value, and a string is a legitimate one.
func structured(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice:
		return t.Elem().Kind() != reflect.Uint8
	case reflect.Array, reflect.Map, reflect.Struct:
		return true
	}
	return false
}

// jsonFields lists the JSON names a decode target accepts, in the order
// they are declared, or "" when v is not a struct pointer.
func jsonFields(v any) string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return ""
	}
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
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

// CallMutator is implemented by a tool that is read-only for most inputs
// but acts on the world for some. Its Mutating reports false, so it is
// still offered in read-only modes and to subagents and runs in the
// parallel batch; MutatesCall names the inputs that must go through the
// permission path, the mode gates and the serial batch like any mutating
// call. See MutatingCall.
type CallMutator interface {
	MutatesCall(input json.RawMessage) bool
}

// MutatingCall reports whether this particular call of t is mutating: the
// tool is mutating as a whole, or it says this input is.
func MutatingCall(t Tool, input json.RawMessage) bool {
	if t.Mutating() {
		return true
	}
	m, ok := t.(CallMutator)
	return ok && m.MutatesCall(input)
}

// Warner is implemented by tools that can flag risky inputs so the
// permission prompt can call them out. Empty means nothing to flag.
type Warner interface {
	Warn(input json.RawMessage) string
}
