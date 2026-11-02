package perm

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/shell"
)

// Typed scopes.
//
// A parameter clause names a field of the call (param.go). A handful of
// names instead name a scope — something the call does, worked out from
// the call whatever tool it is — so one rule can say what a person means
// without knowing which field each tool keeps it in:
//
//	cmd:go test *           bash: the command's argv, word by word
//	read:internal/**        the paths the call reads
//	write:docs/**           the paths the call writes (bash included)
//	host:*.github.com       the network hosts the call contacts
//	touches:**/.env         any path the call names, read or written
//	arg:*BEGIN*PRIVATE*     any string anywhere in the call's input
//	secret:*                the call's input holds a credential
//
// They combine with each other and with field clauses, and every clause
// must hold: bash(cmd:curl *, host:api.github.com).
//
// A scope with several values reads differently in an allow rule than in
// a deny or ask rule, because the two fail in opposite directions. An
// allow rule covers a call only when every value is in scope and there
// is at least one — bash(cmd:curl *, host:api.github.com) does not
// allow a curl that also fetches from evil.example. A deny or ask rule
// catches a call when any value is — *(touches:**/.env) refuses a cat
// that names .env among other files.

// ScopeNames are the parameter names with a typed meaning.
var ScopeNames = []string{"cmd", "read", "write", "host", "touches", "arg", "secret"}

// matchMode is which way a multi-valued scope is read: every value
// (allow) or any value (deny and ask).
type matchMode int

const (
	modeAll matchMode = iota
	modeAny
)

// HostsFunc returns the network hosts a call of one tool contacts.
type HostsFunc func(input json.RawMessage) []string

var (
	hostsMu    sync.RWMutex
	hostsFuncs = map[string]HostsFunc{}
)

// RegisterHosts tells the rules which hosts a tool's calls contact, for
// tools whose host is not in their input: web_search (its provider's
// API) and each remote MCP server's tools (the server's URL). A tool
// with none registered has no host scope beyond bash and fetch_url.
func RegisterHosts(tool string, fn HostsFunc) {
	hostsMu.Lock()
	defer hostsMu.Unlock()
	if fn == nil {
		delete(hostsFuncs, tool)
		return
	}
	hostsFuncs[tool] = fn
}

func registeredHosts(tool string) (HostsFunc, bool) {
	hostsMu.RLock()
	defer hostsMu.RUnlock()
	fn, ok := hostsFuncs[tool]
	return fn, ok
}

// isScope reports whether a clause name is read as a typed scope for
// this tool. cmd only means something for bash, and host only where a
// host can be worked out; elsewhere the name is an ordinary field, so a
// tool that happens to have a "cmd" or "host" field keeps its meaning.
func isScope(name, tool string) bool {
	switch name {
	case "read", "write", "touches", "arg", "secret":
		return true
	case "cmd":
		return tool == "bash"
	case "host":
		if tool == "bash" || tool == "fetch_url" {
			return true
		}
		_, ok := registeredHosts(tool)
		return ok
	}
	return false
}

// readTools read the path they are given; writeTools write it.
var (
	readTools  = map[string]bool{"read_file": true, "list_dir": true, "glob": true, "grep": true, "read_many": true}
	writeTools = map[string]bool{"write_file": true, "edit_file": true, "notebook_edit": true, "ast_edit": true}
)

// scopeMatch matches one typed clause against a call.
func scopeMatch(p Param, c Call, f *fields, mode matchMode) bool {
	switch p.Name {
	case "cmd":
		return cmdMatch(p.Pattern, commandOf(c), c.Root, mode)
	case "host":
		return eachMatch(hostsOf(c), mode, func(h string) bool { return hostMatch(p.Pattern, h) })
	case "read":
		return pathsMatch(p.Pattern, readPaths(c, f), c.Root, mode)
	case "write":
		return pathsMatch(p.Pattern, writePaths(c, f), c.Root, mode)
	case "touches":
		ps := append(readPaths(c, f), writePaths(c, f)...)
		ps = append(ps, loosePaths(c, f)...)
		return pathsMatch(p.Pattern, ps, c.Root, modeAny)
	case "arg":
		for _, s := range allStrings(c, f) {
			if Match(p.Pattern, s) {
				return true
			}
		}
		return false
	case "secret":
		if v := strings.ToLower(p.Pattern); v == "no" || v == "false" {
			return secretIn(c, f) == ""
		}
		return secretIn(c, f) != ""
	}
	return false
}

// eachMatch applies the mode: every value and at least one, or any.
func eachMatch(vals []string, mode matchMode, ok func(string) bool) bool {
	if len(vals) == 0 {
		return false
	}
	for _, v := range vals {
		if ok(v) {
			if mode == modeAny {
				return true
			}
		} else if mode == modeAll {
			return false
		}
	}
	return mode == modeAll
}

// commandOf is a bash call's command line.
func commandOf(c Call) string {
	var in struct {
		Command string `json:"command"`
	}
	if len(c.Input) > 0 && json.Unmarshal(c.Input, &in) == nil && in.Command != "" {
		return shell.TrimWorkspaceCd(strings.TrimSpace(in.Command), c.Root)
	}
	return c.Subject
}

// cmdMatch is the cmd scope. In an allow rule the line must be exactly
// one command, its argv as written must match the pattern word by word
// (no wrapper is looked through: sudo go test is not go test), and
// everything it writes must be inside the workspace — so bash(cmd:go test *)
// allows neither `go test ./... && curl x`, nor `cd /elsewhere && go test`,
// nor `go test > ~/.profile`. A compound line is judged a command at a
// time by the caller (see Rules.allows), which hands each one in alone.
// In a deny or ask rule any command the line runs matches, wrappers,
// substitutions and `sh -c` bodies included.
func cmdMatch(pattern, line, root string, mode matchMode) bool {
	pw := shell.Words(pattern)
	if len(pw) == 0 || strings.TrimSpace(line) == "" {
		return false
	}
	if mode == modeAny {
		for _, argv := range shell.Argvs(line) {
			if argvMatch(pw, argv) {
				return true
			}
		}
		for _, seg := range shell.Commands(line) {
			if argvMatch(pw, shell.RawArgv(seg)) {
				return true
			}
		}
		return false
	}
	cmds := shell.Commands(line)
	if len(cmds) != 1 {
		return false
	}
	if !argvMatch(pw, stripRedirects(shell.RawArgv(cmds[0]))) {
		return false
	}
	if root != "" {
		for _, w := range shell.WriteTargets(line, root) {
			if outside(w, root) {
				return false
			}
		}
	}
	return true
}

// stripRedirects drops redirection words (2>&1, > out) from an argv: the
// write they make is judged separately, and a pattern should not have to
// spell them.
func stripRedirects(argv []string) []string {
	out := argv[:0:0]
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case dupRedirect.MatchString(a) && dupRedirect.FindString(a) == a:
		case a == ">" || a == ">>" || a == "2>" || a == "&>" || a == "<" || a == "2>>":
			i++
		case strings.HasPrefix(a, ">") || strings.HasPrefix(a, "2>") || strings.HasPrefix(a, "&>") || strings.HasPrefix(a, "<"):
		default:
			out = append(out, a)
		}
	}
	return out
}

// argvMatch matches pattern words against argv words: each pattern word
// is a glob within one word, and a final bare * matches any number of
// remaining words, none included. The program name may be written as a
// path in the command (/usr/bin/go) and still match a pattern naming it
// only when the pattern gives the path too.
func argvMatch(pw, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	for i, p := range pw {
		if p == "*" && i == len(pw)-1 {
			return true
		}
		if i >= len(argv) || !glob(p, argv[i]) {
			return false
		}
	}
	return len(argv) == len(pw)
}

// hostsOf is the network hosts a call contacts.
func hostsOf(c Call) []string {
	switch c.Tool {
	case "bash":
		return shell.NetworkHosts(commandOf(c))
	case "fetch_url":
		return urlHost(c.Input)
	}
	if fn, ok := registeredHosts(c.Tool); ok {
		return fn(c.Input)
	}
	return nil
}

// hostMatch matches a host pattern: a glob, case-insensitive, where
// *.example.com also covers example.com itself. A pattern without a port
// matches the host on any port; one with a port only that port.
func hostMatch(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(host)
	if !hasPort(pattern) {
		host = stripPort(host)
	}
	if glob(pattern, host) {
		return true
	}
	return strings.HasPrefix(pattern, "*.") && host == pattern[2:]
}

func hasPort(h string) bool {
	_, port, err := net.SplitHostPort(h)
	return err == nil && port != ""
}

func stripPort(h string) string {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		return strings.Trim(hh, "[]")
	}
	return h
}

// urlHost is a fetch_url call's host as contacted, with its port when the
// URL names one: one value per call, so an allow rule's every-value test
// is not failed by a second spelling of the same host.
func urlHost(input json.RawMessage) []string {
	hs := urlHosts(input)
	if len(hs) == 0 {
		return nil
	}
	return hs[:1]
}

// readPaths are the paths a call reads.
func readPaths(c Call, f *fields) []string {
	switch {
	case readTools[c.Tool]:
		ps := f.values("path")
		if c.Tool == "read_many" {
			ps = append(ps, f.values("paths")...)
		}
		if len(ps) == 0 && (c.Tool == "glob" || c.Tool == "grep") {
			ps = []string{"."}
		}
		return ps
	case c.Tool == "bash":
		line := commandOf(c)
		writes := map[string]bool{}
		for _, w := range shell.WriteTargets(line, c.Root) {
			writes[w] = true
		}
		var out []string
		for _, p := range shell.PathWords(line) {
			if !writes[absIn(p, c.Root)] {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// writePaths are the paths a call writes.
func writePaths(c Call, f *fields) []string {
	switch {
	case writeTools[c.Tool]:
		return f.values("path")
	case c.Tool == "bash":
		return shell.WriteTargets(commandOf(c), c.Root)
	}
	return nil
}

// loosePaths are the path-looking strings of a call to any other tool —
// an MCP server's file tool names its path however it likes.
func loosePaths(c Call, f *fields) []string {
	if readTools[c.Tool] || writeTools[c.Tool] || c.Tool == "bash" {
		return nil
	}
	var out []string
	for _, s := range allStrings(c, f) {
		if len(s) < 4096 && !strings.ContainsAny(s, "\n") && strings.ContainsAny(s, "/.~") && !strings.Contains(s, "://") {
			out = append(out, s)
		}
	}
	return out
}

// allStrings is every string value in the call's input, at any depth.
func allStrings(c Call, f *fields) []string {
	if !f.done {
		f.done = true
		if json.Unmarshal(f.input, &f.decoded) != nil {
			f.decoded = nil
		}
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(f.decoded)
	if len(out) == 0 && c.Subject != "" {
		out = []string{c.Subject}
	}
	return out
}

func secretIn(c Call, f *fields) string {
	for _, s := range allStrings(c, f) {
		if what := secret.Scan(s); what != "" {
			return what
		}
	}
	return ""
}

// pathsMatch matches a path pattern against the paths of a call. Each
// path is tried in every spelling it has — as written, relative to or
// absolute in the workspace, and with its symlinks resolved — and:
//
//   - in an allow rule (every path, and at least one) a path is in scope
//     only when both what it is written as and what it resolves to are,
//     so a symlink inside the scope that points out of it, or a path
//     that climbs out with .., is not covered;
//   - in a deny or ask rule a path is caught when any spelling is.
func pathsMatch(pattern string, paths []string, root string, mode matchMode) bool {
	pattern = expandHome(pattern)
	return eachMatch(paths, mode, func(p string) bool {
		given := spellings(p, root)
		resolved := resolvedSpellings(p, root)
		if mode == modeAny {
			for _, s := range append(given, resolved...) {
				if pathMatch(pattern, s) {
					return true
				}
			}
			return false
		}
		return anyPath(pattern, given) && (len(resolved) == 0 || anyPath(pattern, resolved))
	})
}

func anyPath(pattern string, ss []string) bool {
	for _, s := range ss {
		if pathMatch(pattern, s) {
			return true
		}
	}
	return false
}

// pathMatch is Match for paths: separators are compared as /, and a
// leading **/ also matches at the top (**/.env matches .env).
func pathMatch(pattern, p string) bool {
	pattern, p = filepath.ToSlash(pattern), filepath.ToSlash(p)
	if Match(pattern, p) {
		return true
	}
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok && Match(rest, p) {
		return true
	}
	// dir/** also names dir itself.
	if base, ok := strings.CutSuffix(pattern, "/**"); ok && Match(base, p) {
		return true
	}
	return false
}

// spellings are the ways a path is written: cleaned, ~ expanded, and its
// other spelling relative to the workspace.
func spellings(p, root string) []string {
	p = filepath.Clean(expandHome(p))
	out := []string{p}
	if root != "" {
		root = filepath.Clean(root)
		if filepath.IsAbs(p) {
			if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				out = append(out, rel)
			}
		} else {
			out = append(out, filepath.Join(root, p))
		}
	}
	return out
}

// resolvedSpellings are the spellings of what p resolves to through
// symlinks, or nil when it resolves to itself or cannot be resolved
// (a file about to be created resolves through its parent).
func resolvedSpellings(p, root string) []string {
	abs := absIn(expandHome(p), root)
	if abs == "" {
		return nil
	}
	res, err := filepath.EvalSymlinks(abs)
	if err != nil {
		dir, err2 := filepath.EvalSymlinks(filepath.Dir(abs))
		if err2 != nil {
			return nil
		}
		res = filepath.Join(dir, filepath.Base(abs))
	}
	rroot := root
	if root != "" {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			rroot = r
		}
	}
	if res == abs {
		return nil
	}
	out := []string{res}
	if rroot != "" {
		if rel, err := filepath.Rel(rroot, res); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, rel)
		}
	}
	return out
}

func absIn(p, root string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if root == "" {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return ""
	}
	return filepath.Join(root, p)
}

func outside(p, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), absIn(p, root))
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
