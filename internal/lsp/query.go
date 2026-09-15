package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Queries a language server answers about the code itself, rather than
// about its problems: where a name is defined, who uses it, what its type
// is, what a file contains. The agent can get close to these with grep,
// but grep matches text — it cannot tell a definition from a mention, a
// method from a field of the same name, or a use of this Config from a
// use of another package's. The server already has the resolved program.

// Location is a place in the code, with the source line for context.
// Line and Col are 1-based, as they are shown.
type Location struct {
	Path string
	Line int
	Col  int
	Text string
}

// String renders the location the way a compiler would.
func (l Location) String() string {
	s := fmt.Sprintf("%s:%d:%d", l.Path, l.Line, l.Col)
	if l.Text != "" {
		s += ": " + strings.TrimSpace(l.Text)
	}
	return s
}

// Symbol is a named thing in a file or workspace.
type Symbol struct {
	Name      string
	Kind      string
	Container string
	Loc       Location
}

// queryTimeout bounds one request; a server that has to index a large
// project answers the first query slowly.
func (m *Manager) queryTimeout() time.Duration {
	t := time.Duration(m.Config.Timeout) * time.Second
	if t <= 0 {
		t = 8 * time.Second
	}
	return t
}

// open makes sure the server has the file's current text, then returns the
// client and the file's lines. Queries are answered against the open
// document, so an unsaved or just-edited file resolves correctly.
func (m *Manager) open(ctx context.Context, path string) (*client, string, []string, error) {
	if !m.Enabled() {
		return nil, "", nil, nil
	}
	c, err := m.clientFor(ctx, path)
	if err != nil || c == nil {
		return nil, "", nil, err
	}
	abs := m.abs(path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", nil, err
	}
	if err := c.sync(abs, string(data)); err != nil {
		return nil, "", nil, err
	}
	return c, abs, strings.Split(string(data), "\n"), nil
}

// sync makes sure the server has the file's current text. Unlike diagnose
// it does not wait for diagnostics: a query only needs the text to be
// there.
//
// A document whose content has not changed is not sent again. Every query
// used to re-send the whole file, so answering six questions about one
// file made the server re-parse it six times — wasted work on every
// query, and enough of it to make clangd stop answering.
func (c *client) sync(abs, text string) error {
	uri := toURI(abs)
	sum := sha256.Sum256([]byte(text))
	c.docM.Lock()
	version, open := c.docs[uri]
	if open && c.synced[uri] == sum {
		c.docM.Unlock()
		return nil
	}
	version++
	c.docs[uri] = version
	if c.synced == nil {
		c.synced = map[string][32]byte{}
	}
	c.synced[uri] = sum
	c.docM.Unlock()
	if !open {
		return c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": languageID(abs), "version": version, "text": text}})
	}
	return c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// Point resolves where in a file a query points. A caller gives a line
// (and optionally a column), or a symbol name to find in the file.
type Point struct {
	Line   int // 1-based; 0 means find Symbol
	Col    int // 1-based; 0 means the first non-space of the line
	Symbol string
}

// resolve turns a Point into 0-based LSP coordinates against lines.
func resolve(p Point, lines []string) (int, int, error) {
	if p.Line > 0 {
		if p.Line > len(lines) {
			return 0, 0, fmt.Errorf("line %d is past the end of the file (%d lines)", p.Line, len(lines))
		}
		text := lines[p.Line-1]
		col := p.Col - 1
		if col < 0 {
			col = len(text) - len(strings.TrimLeft(text, " \t"))
			// Point at the first identifier on the line, which is what a
			// caller naming only a line almost always means.
			if loc := identRe.FindStringIndex(text); loc != nil {
				col = loc[0]
			}
		}
		return p.Line - 1, col, nil
	}
	if p.Symbol == "" {
		return 0, 0, fmt.Errorf("give a line or a symbol")
	}
	// Where the name occurs as a whole identifier. The first mention is
	// usually in the doc comment above the declaration, and a server
	// answers nothing for a position inside a comment, so occurrences are
	// ranked: a declaration line first, then any code line, and a comment
	// only if there is nothing else.
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(p.Symbol) + `\b`)
	if err != nil {
		return 0, 0, err
	}
	best, bestRank := [2]int{-1, -1}, -1
	for i, l := range lines {
		loc := re.FindStringIndex(l)
		if loc == nil {
			continue
		}
		rank := 1
		if inComment(l, loc[0]) {
			rank = 0
		} else if declRe.MatchString(l[:loc[0]]) {
			rank = 2
		}
		if rank > bestRank {
			best, bestRank = [2]int{i, loc[0]}, rank
		}
		if bestRank == 2 {
			break
		}
	}
	if bestRank < 0 {
		return 0, 0, fmt.Errorf("%q does not appear in the file", p.Symbol)
	}
	return best[0], best[1], nil
}

// declRe matches the part of a line before a name when that name is being
// declared, across the languages a server is usually configured for.
var declRe = regexp.MustCompile(`(^|\s)(func|type|class|struct|interface|def|const|var|let|enum|trait|impl|fn|module|package)\s+(\(.*\)\s*)?$`)

// inComment reports whether the byte at col is inside a line comment.
func inComment(line string, col int) bool {
	trimmed := strings.TrimSpace(line)
	for _, p := range []string{"//", "#", "*", "--", ";"} {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	for _, marker := range []string{"//", "#"} {
		if i := strings.Index(line, marker); i >= 0 && i < col {
			return true
		}
	}
	return false
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// Definition is where the symbol at pos is declared.
func (m *Manager) Definition(ctx context.Context, path string, pos Point) ([]Location, error) {
	return m.locationQuery(ctx, "textDocument/definition", path, pos, nil)
}

// Implementation is where an interface or abstract method is implemented.
func (m *Manager) Implementation(ctx context.Context, path string, pos Point) ([]Location, error) {
	return m.locationQuery(ctx, "textDocument/implementation", path, pos, nil)
}

// TypeDefinition is where the type of the symbol at pos is declared.
func (m *Manager) TypeDefinition(ctx context.Context, path string, pos Point) ([]Location, error) {
	return m.locationQuery(ctx, "textDocument/typeDefinition", path, pos, nil)
}

// References is every use of the symbol at pos, including its declaration.
//
// A server only reports uses in files it knows about, and some know only
// what has been opened — tsserver and clangd both answer a cold project
// with the declaration alone, which reads as "used nowhere" and is worse
// than an error. The project's other files of the same language are
// opened first, so the answer covers the project rather than the file.
func (m *Manager) References(ctx context.Context, path string, pos Point) ([]Location, error) {
	m.warm(ctx, path)
	return m.locationQuery(ctx, "textDocument/references", path, pos, map[string]any{
		"context": map[string]any{"includeDeclaration": true},
	})
}

// warmLimit bounds how many sibling files are opened before a project-wide
// query: enough for a real answer, few enough not to stall on a monorepo.
const (
	warmLimit    = 60
	warmMaxBytes = 512 * 1024
)

// warm tells the server about the project's other files in the same
// language, so a project-wide question is answered about the project. It
// is done once per language server and is best-effort throughout: a file
// that cannot be read is skipped, and a server that does not need this
// pays one pass over the tree.
func (m *Manager) warm(ctx context.Context, path string) {
	c, err := m.clientFor(ctx, path)
	if err != nil || c == nil {
		return
	}
	c.warmOnce.Do(func() {
		ext := strings.ToLower(filepath.Ext(path))
		if ext == "" {
			return
		}
		opened := 0
		_ = filepath.WalkDir(m.root(), func(p string, d os.DirEntry, err error) error {
			if err != nil || opened >= warmLimit {
				if opened >= warmLimit {
					return filepath.SkipAll
				}
				return nil
			}
			if d.IsDir() {
				// The places a project keeps things that are not its source.
				switch d.Name() {
				case ".git", "node_modules", "vendor", "target", "dist", "build", ".venv", "__pycache__":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(p), ext) {
				return nil
			}
			if fi, err := d.Info(); err != nil || fi.Size() > warmMaxBytes {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if err := c.sync(p, string(data)); err == nil {
				opened++
			}
			return nil
		})
	})
}

func (m *Manager) locationQuery(ctx context.Context, method, path string, pos Point, extra map[string]any) ([]Location, error) {
	c, abs, lines, err := m.open(ctx, path)
	if err != nil || c == nil {
		return nil, err
	}
	line, col, err := resolve(pos, lines)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": toURI(abs)},
		"position":     map[string]any{"line": line, "character": col},
	}
	for k, v := range extra {
		params[k] = v
	}
	// A server that is still indexing answers immediately and says
	// nothing, which is indistinguishable from "there is no such symbol"
	// and far worse: the model reads it as a fact. rust-analyzer on a cold
	// Cargo project does exactly this. An empty answer is retried, briefly
	// and with a widening gap, before it is believed.
	locs, err := m.retryEmpty(ctx, func(qctx context.Context) ([]Location, error) {
		raw, err := c.call(qctx, method, params)
		if err != nil {
			return nil, err
		}
		return m.decodeLocations(raw), nil
	})
	return locs, err
}

// Codes a server uses for "ask again", not for "no". The protocol
// defines both: ContentModified when the document changed while the
// request was in flight, RequestCancelled when the server dropped it.
// Neither is a fact about the code, and passing either to the model as an
// error is worse than saying nothing.
const (
	codeContentModified  = -32801
	codeRequestCancelled = -32800
)

// transient reports whether an error is the server asking to be asked
// again rather than answering.
func transient(err error) bool {
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == codeContentModified || re.Code == codeRequestCancelled
}

// retryEmpty runs a query until it returns something, the deadline
// passes, or it errors. A server with a genuine empty answer costs the
// extra wait once; a server that was not ready gets the chance to become
// ready, which is the difference between a right answer and a wrong one.
func (m *Manager) retryEmpty(ctx context.Context, q func(context.Context) ([]Location, error)) ([]Location, error) {
	deadline := time.Now().Add(m.queryTimeout())
	wait := 250 * time.Millisecond
	for attempt := 0; ; attempt++ {
		qctx, cancel := context.WithDeadline(ctx, deadline)
		locs, err := q(qctx)
		cancel()
		if err != nil && !transient(err) {
			return nil, err
		}
		if err == nil && len(locs) > 0 {
			return locs, nil
		}
		if attempt >= maxEmptyRetries || time.Now().Add(wait).After(deadline) {
			return nil, nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		wait *= 2
	}
}

// maxEmptyRetries bounds the wait for a server that is still indexing.
const maxEmptyRetries = 4

// Hover is the type and documentation of the symbol at pos.
func (m *Manager) Hover(ctx context.Context, path string, pos Point) (string, error) {
	c, abs, lines, err := m.open(ctx, path)
	if err != nil || c == nil {
		return "", err
	}
	line, col, err := resolve(pos, lines)
	if err != nil {
		return "", err
	}
	qctx, cancel := context.WithTimeout(ctx, m.queryTimeout())
	defer cancel()
	raw, err := c.call(qctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": toURI(abs)},
		"position":     map[string]any{"line": line, "character": col},
	})
	if transient(err) {
		return "", nil // the server asked to be asked again; say nothing rather than erroring
	}
	if err != nil {
		return "", err
	}
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return "", nil
	}
	return hoverText(h.Contents), nil
}

// hoverText flattens the several shapes hover contents can take: a
// MarkupContent, a marked string, or a list of either.
func hoverText(raw json.RawMessage) string {
	var mc struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &mc) == nil && mc.Value != "" {
		return strings.TrimSpace(mc.Value)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var parts []string
		for _, item := range list {
			if t := hoverText(item); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// Outline is the symbols declared in one file, in source order.
func (m *Manager) Outline(ctx context.Context, path string) ([]Symbol, error) {
	c, abs, lines, err := m.open(ctx, path)
	if err != nil || c == nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, m.queryTimeout())
	defer cancel()
	raw, err := c.call(qctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": toURI(abs)},
	})
	if transient(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rel := m.rel(abs)
	var out []Symbol
	// Servers answer either a flat SymbolInformation list or a nested
	// DocumentSymbol tree; both shapes are accepted.
	var nested []documentSymbol
	if json.Unmarshal(raw, &nested) == nil && len(nested) > 0 && nested[0].Name != "" && nested[0].Location.URI == "" {
		var walk func(ds []documentSymbol, parent string)
		walk = func(ds []documentSymbol, parent string) {
			for _, d := range ds {
				out = append(out, Symbol{
					Name: d.Name, Kind: symbolKind(d.Kind), Container: parent,
					Loc: lineLoc(rel, lines, d.SelectionRange.Start.Line, d.SelectionRange.Start.Character),
				})
				walk(d.Children, d.Name)
			}
		}
		walk(nested, "")
		return out, nil
	}
	var flat []symbolInformation
	if json.Unmarshal(raw, &flat) == nil {
		for _, s := range flat {
			out = append(out, Symbol{
				Name: s.Name, Kind: symbolKind(s.Kind), Container: s.ContainerName,
				Loc: lineLoc(m.rel(fromURI(s.Location.URI)), lines, s.Location.Range.Start.Line, s.Location.Range.Start.Character),
			})
		}
	}
	return out, nil
}

// WorkspaceSymbols finds symbols by name across the project. hint names a
// file whose language server should answer; without one the first running
// server is used.
func (m *Manager) WorkspaceSymbols(ctx context.Context, query, hint string) ([]Symbol, error) {
	c, err := m.queryClient(ctx, hint)
	if err != nil || c == nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, m.queryTimeout())
	defer cancel()
	raw, err := c.call(qctx, "workspace/symbol", map[string]any{"query": query})
	if transient(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var flat []symbolInformation
	if json.Unmarshal(raw, &flat) != nil {
		return nil, nil
	}
	out := make([]Symbol, 0, len(flat))
	for _, s := range flat {
		p := m.rel(fromURI(s.Location.URI))
		out = append(out, Symbol{
			Name: s.Name, Kind: symbolKind(s.Kind), Container: s.ContainerName,
			Loc: Location{Path: p, Line: s.Location.Range.Start.Line + 1, Col: s.Location.Range.Start.Character + 1},
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Loc.Path < out[j].Loc.Path })
	return out, nil
}

// queryClient picks the server to ask a workspace-wide question.
func (m *Manager) queryClient(ctx context.Context, hint string) (*client, error) {
	if hint != "" {
		return m.clientFor(ctx, hint)
	}
	// Map iteration order is random, so with two servers up the same
	// question went to a different language's server each time it was
	// asked and got a different answer. Take the first by name instead:
	// arbitrary, but the same arbitrary choice every time.
	m.mu.Lock()
	first := ""
	for name := range m.clients {
		if first == "" || name < first {
			first = name
		}
	}
	c := m.clients[first]
	m.mu.Unlock()
	if c != nil {
		return c, nil
	}
	// Nothing running yet: start the server for any file type it handles,
	// by naming a file with each configured extension.
	for _, s := range m.Config.Active() {
		for _, ext := range s.Extensions {
			if c, err := m.clientFor(ctx, "x"+ext); err == nil && c != nil {
				return c, nil
			}
		}
	}
	return nil, nil
}

// decodeLocations accepts the three shapes a location result can take: one
// Location, a list of them, or a list of LocationLinks.
func (m *Manager) decodeLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	add := func(out []Location, uri string, line, col int) []Location {
		p := m.rel(fromURI(uri))
		return append(out, Location{Path: p, Line: line + 1, Col: col + 1, Text: m.sourceLine(p, line)})
	}
	var one lspLocation
	if json.Unmarshal(raw, &one) == nil && one.URI != "" {
		return add(nil, one.URI, one.Range.Start.Line, one.Range.Start.Character)
	}
	var list []lspLocation
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 && list[0].URI != "" {
		var out []Location
		for _, l := range list {
			out = add(out, l.URI, l.Range.Start.Line, l.Range.Start.Character)
		}
		return out
	}
	var links []locationLink
	if json.Unmarshal(raw, &links) == nil {
		var out []Location
		for _, l := range links {
			r := l.TargetSelectionRange
			if r.Start.Line == 0 && r.End.Line == 0 {
				r = l.TargetRange
			}
			out = add(out, l.TargetURI, r.Start.Line, r.Start.Character)
		}
		return out
	}
	return nil
}

// sourceLine reads one line of a file for context in a result.
func (m *Manager) sourceLine(path string, line int) string {
	data, err := os.ReadFile(m.abs(path))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

// rel makes a path relative to the project root, so results read the way
// the model's own paths do.
func (m *Manager) rel(abs string) string {
	root := m.root()
	if root == "" || !filepath.IsAbs(abs) {
		return abs
	}
	if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return abs
}

func lineLoc(path string, lines []string, line, col int) Location {
	l := Location{Path: path, Line: line + 1, Col: col + 1}
	if line >= 0 && line < len(lines) {
		l.Text = lines[line]
	}
	return l
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type locationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetRange          lspRange `json:"targetRange"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
}

type symbolInformation struct {
	Name          string      `json:"name"`
	Kind          int         `json:"kind"`
	ContainerName string      `json:"containerName"`
	Location      lspLocation `json:"location"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail"`
	Kind           int              `json:"kind"`
	Range          lspRange         `json:"range"`
	SelectionRange lspRange         `json:"selectionRange"`
	Children       []documentSymbol `json:"children"`
	// Location is set only on the flat SymbolInformation shape; its
	// presence is how the two are told apart.
	Location lspLocation `json:"location"`
}

// symbolKinds maps the protocol's SymbolKind numbers to words.
var symbolKinds = map[int]string{
	1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method",
	7: "property", 8: "field", 9: "constructor", 10: "enum", 11: "interface",
	12: "function", 13: "variable", 14: "constant", 15: "string", 16: "number",
	17: "boolean", 18: "array", 19: "object", 20: "key", 21: "null",
	22: "enum member", 23: "struct", 24: "event", 25: "operator", 26: "type parameter",
}

func symbolKind(k int) string {
	if s, ok := symbolKinds[k]; ok {
		return s
	}
	return "symbol"
}
