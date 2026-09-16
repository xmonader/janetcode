// Package rules loads conditional project instructions: markdown files
// that apply only to the paths they name.
//
// Instructions that are always in the prompt are paid for on every
// request of every session, which is why a project's JANETCODE.md has to
// stay short — and why the things worth saying about one corner of a
// repository ("migrations here are append-only", "this package is
// generated, edit the .proto") usually go unsaid. A rule says them where
// they belong: nothing is sent until a tool call touches a matching
// path, and then it is sent once for the session.
package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/md"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Rule is one conditional instruction file.
type Rule struct {
	// Name is the file's stem, or the frontmatter's name.
	Name string
	// Description is the one-line summary shown by /rules.
	Description string
	// Paths are the globs that bring the rule in. A rule with no globs
	// never applies; instructions that always apply belong in
	// JANETCODE.md, where their cost is visible.
	Paths []string
	Body  string
	Path  string
	// Source is "project", "global" or "claude".
	Source string
}

const (
	// maxRuleBytes bounds one rule, so a file nobody meant as a prompt
	// cannot take the context window with it.
	maxRuleBytes = 8 * 1024
	// maxRules bounds a rules directory, so a checkout with a thousand
	// markdown files under .claude/rules cannot be matched against on
	// every tool call.
	maxRules = 64
)

// ruleDir is one directory rules are read from, in load order: later
// sources shadow earlier ones by rule name.
type ruleDir struct{ dir, source string }

// dirs is the search path for rules visible from cwd.
func dirs(cwd, configDir string) []ruleDir {
	var out []ruleDir
	if configDir != "" {
		out = append(out, ruleDir{filepath.Join(configDir, "rules"), "global"})
	}
	if cwd != "" {
		out = append(out,
			ruleDir{filepath.Join(cwd, ".claude", "rules"), "claude"},
			ruleDir{filepath.Join(cwd, ".janetcode", "rules"), "project"})
	}
	return out
}

// Load reads every rule visible from cwd: .janetcode/rules/*.md in the
// project, rules/ under the config directory, and .claude/rules for
// compatibility. A project rule shadows a global one of the same name.
// Unreadable files, empty files and files that name no globs are
// skipped; a rules directory is never a reason to fail.
func Load(cwd, configDir string) []Rule {
	byName := map[string]Rule{}
	for _, l := range dirs(cwd, configDir) {
		entries, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		read := 0
		for _, e := range entries {
			if read >= maxRules {
				break
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			read++
			r, ok := parse(filepath.Join(l.dir, e.Name()), e.Name(), l.source)
			if !ok {
				continue
			}
			byName[r.Name] = r
		}
	}
	out := make([]Rule, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parse turns one markdown file into a rule. ok is false for anything
// that could not be read, has no body, or names no paths.
func parse(path, fileName, source string) (Rule, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Rule{}, false
	}
	meta, body := md.Parse(string(data))
	body = strings.TrimSpace(body)
	if body == "" {
		return Rule{}, false
	}
	body = truncate(body, maxRuleBytes)
	r := Rule{
		Name:        strings.TrimSuffix(fileName, ".md"),
		Description: strings.TrimSpace(meta["description"]),
		Paths:       normGlobs(md.List(meta["paths"])),
		Body:        body,
		Path:        path,
		Source:      source,
	}
	if n := strings.TrimSpace(meta["name"]); n != "" {
		r.Name = n
	}
	if r.Description == "" {
		r.Description = textfmt.HeadingLine(body, 0)
	}
	if len(r.Paths) == 0 {
		return Rule{}, false
	}
	return r, true
}

// truncate cuts a body to at most n bytes without splitting a rune (or,
// where one is close by, a line).
func truncate(body string, n int) string {
	if len(body) <= n {
		return body
	}
	cut := body[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexByte(cut, '\n'); i > n-512 {
		cut = cut[:i]
	}
	return cut + "\n… (truncated)"
}

// normGlobs trims and slash-normalises globs once, at load, so Matches
// does not redo it for every path of every tool call.
func normGlobs(globs []string) []string {
	out := make([]string, 0, len(globs))
	for _, g := range globs {
		if g = normPath(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// normPath is the shared spelling of a path or glob: slash-separated,
// trimmed, without a leading "./".
func normPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}

// Matches reports whether a path the model touched brings the rule in.
// Paths are compared as slash-separated and relative to the project, and
// a glob without a slash is matched against the base name too, so
// "*.sql" covers db/migrations/001.sql.
func (r Rule) Matches(path string) bool {
	path = normPath(path)
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, g := range r.Paths {
		// Rules built by Load are normalised already; one built by hand
		// (a test, a caller) may not be.
		g = normPath(g)
		if g == "" {
			continue
		}
		if perm.Match(g, path) {
			return true
		}
		if !strings.Contains(g, "/") && perm.Match(g, base) {
			return true
		}
	}
	return false
}

// Text is what the model is shown when a rule comes in.
func (r Rule) Text() string {
	return fmt.Sprintf("[project rule: %s — applies to %s]\n%s", r.Name, strings.Join(r.Paths, ", "), r.Body)
}

// Injector hands each rule to the model at most once per session.
type Injector struct {
	// Rules is re-read on every call so a rule added mid-session works,
	// like skills and memory. It is called without the lock held: it
	// touches the disk, and /rules must not block behind it.
	Rules func() []Rule

	// mu guards sent, and nothing else.
	mu   sync.Mutex
	sent map[string]bool
}

// For returns the text of every rule these paths bring in that has not
// been sent yet, and records that it has been.
func (i *Injector) For(paths []string) []string {
	if i == nil || i.Rules == nil || len(paths) == 0 {
		return nil
	}
	all := i.Rules()
	if len(all) == 0 {
		return nil
	}
	norm := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = normPath(p); p != "" {
			norm = append(norm, p)
		}
	}
	if len(norm) == 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sent == nil {
		i.sent = map[string]bool{}
	}
	var out []string
	for _, r := range all {
		if i.sent[r.Name] {
			continue
		}
		for _, p := range norm {
			if r.Matches(p) {
				i.sent[r.Name] = true
				out = append(out, r.Text())
				break
			}
		}
	}
	return out
}

// Sent reports whether a rule has already been given to the model this
// session, for /rules to say so.
func (i *Injector) Sent(name string) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.sent[name]
}
