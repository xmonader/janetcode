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

// maxRuleBytes bounds one rule, so a file nobody meant as a prompt
// cannot take the context window with it.
const maxRuleBytes = 8 * 1024

// Load reads every rule visible from cwd: .janetcode/rules/*.md in the
// project, rules/ under the config directory, and .claude/rules for
// compatibility. A project rule shadows a global one of the same name.
func Load(cwd, configDir string) []Rule {
	byName := map[string]Rule{}
	type loc struct{ dir, source string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "rules"), "global"})
	}
	if cwd != "" {
		locs = append(locs, loc{filepath.Join(cwd, ".claude", "rules"), "claude"}, loc{filepath.Join(cwd, ".janetcode", "rules"), "project"})
	}
	for _, l := range locs {
		entries, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := filepath.Join(l.dir, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			meta, body := md.Parse(string(data))
			body = strings.TrimSpace(body)
			if body == "" {
				continue
			}
			if len(body) > maxRuleBytes {
				body = body[:maxRuleBytes] + "\n… (truncated)"
			}
			r := Rule{
				Name:        strings.TrimSuffix(e.Name(), ".md"),
				Description: strings.TrimSpace(meta["description"]),
				Paths:       md.List(meta["paths"]),
				Body:        body,
				Path:        p,
				Source:      l.source,
			}
			if n := strings.TrimSpace(meta["name"]); n != "" {
				r.Name = n
			}
			if r.Description == "" {
				r.Description = textfmt.HeadingLine(body, 0)
			}
			if len(r.Paths) == 0 {
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

// Matches reports whether a path the model touched brings the rule in.
// Paths are compared as slash-separated and relative to the project, and
// a glob without a slash is matched against the base name too, so
// "*.sql" covers db/migrations/001.sql.
func (r Rule) Matches(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(path)), "./")
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, g := range r.Paths {
		g = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./")
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
	// like skills and memory.
	Rules func() []Rule

	mu   sync.Mutex
	sent map[string]bool
}

// For returns the text of every rule these paths bring in that has not
// been sent yet, and records that it has been.
func (i *Injector) For(paths []string) []string {
	if i == nil || i.Rules == nil || len(paths) == 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sent == nil {
		i.sent = map[string]bool{}
	}
	var out []string
	for _, r := range i.Rules() {
		if i.sent[r.Name] {
			continue
		}
		for _, p := range paths {
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
