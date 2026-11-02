package rules

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cursor keeps project rules in .cursor/rules/**/*.mdc (or .md): a
// frontmatter block with alwaysApply, globs and description, then the
// body. Two of its four kinds map onto what janetcode already has:
//
//   - alwaysApply: true is an always-on instruction, which belongs in the
//     project instructions beside AGENTS.md (see CursorAlways);
//   - globs are a path-scoped rule, sent once per session after a tool
//     touches a matching path (Load reads them, the Injector sends them).
//
// A rule with only a description (the model "fetches" it in Cursor) or
// with nothing is manual in Cursor and is skipped here: nothing would
// bring it in.

// CursorDir is where a project's Cursor rules live, relative to it.
const CursorDir = ".cursor/rules"

// maxCursorFiles bounds the walk of a rules directory.
const maxCursorFiles = 128

// CursorRule is one parsed .cursor/rules file.
type CursorRule struct {
	// Rel is the path relative to the project, slash-separated.
	Rel         string
	Path        string
	Always      bool
	Globs       []string
	Description string
	Body        string
}

// CursorRules reads dir/.cursor/rules. Unreadable and empty files are
// skipped; the walk is bounded and sorted so the prompt is stable.
func CursorRules(dir string) []CursorRule {
	if dir == "" {
		return nil
	}
	root := filepath.Join(dir, filepath.FromSlash(CursorDir))
	var files []string
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries { // ReadDir is sorted by name
			p := filepath.Join(d, e.Name())
			switch {
			case e.IsDir() && depth < 4:
				walk(p, depth+1)
			case e.Type().IsRegular():
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if (ext == ".mdc" || ext == ".md") && len(files) < maxCursorFiles {
					files = append(files, p)
				}
			}
		}
	}
	walk(root, 0)
	var out []CursorRule
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r := ParseCursor(string(data))
		if r.Body == "" {
			continue
		}
		r.Path = p
		if rel, err := filepath.Rel(dir, p); err == nil {
			r.Rel = filepath.ToSlash(rel)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// ParseCursor reads one rule file's text. Frontmatter is the loose YAML
// subset Cursor writes: scalars, a comma-separated globs string, or a
// "- item" list.
func ParseCursor(text string) CursorRule {
	var r CursorRule
	text = strings.TrimPrefix(text, "\xEF\xBB\xBF")
	body := text
	if strings.HasPrefix(text, "---\n") || strings.HasPrefix(text, "---\r\n") {
		rest := text[strings.Index(text, "\n")+1:]
		if end := strings.Index("\n"+rest, "\n---"); end >= 0 {
			block := rest[:max(end-1, 0)]
			body = ""
			if nl := strings.Index(rest[end:], "\n"); nl >= 0 {
				body = rest[end+nl+1:]
			}
			r.Always, r.Globs, r.Description = cursorFrontmatter(block)
		}
	}
	r.Body = truncate(strings.TrimSpace(body), maxRuleBytes)
	return r
}

func cursorFrontmatter(block string) (always bool, globs []string, desc string) {
	inGlobs := false
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		trim := strings.TrimSpace(line)
		if inGlobs && strings.HasPrefix(trim, "- ") {
			globs = append(globs, splitGlobs(trim[2:])...)
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		inGlobs = false
		v = unquote(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "alwaysApply":
			always = strings.EqualFold(v, "true")
		case "description":
			desc = v
		case "globs":
			if v == "" {
				inGlobs = true
			} else {
				globs = append(globs, splitGlobs(strings.Trim(v, "[]"))...)
			}
		}
	}
	return always, globs, desc
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// splitGlobs splits on commas (Cursor's spelling), not on spaces: a glob
// may hold one.
func splitGlobs(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = unquote(strings.TrimSpace(g)); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// cursorLoad turns the project's glob-scoped Cursor rules into Rules.
// Names carry a "cursor:" prefix so one cannot shadow a janetcode rule.
func cursorLoad(cwd string) []Rule {
	var out []Rule
	for _, c := range CursorRules(cwd) {
		if c.Always || len(c.Globs) == 0 {
			continue
		}
		name := "cursor:" + strings.TrimSuffix(strings.TrimPrefix(c.Rel, CursorDir+"/"), filepath.Ext(c.Rel))
		desc := c.Description
		if desc == "" {
			desc = name
		}
		out = append(out, Rule{Name: name, Description: desc, Paths: normGlobs(c.Globs), Body: c.Body, Path: c.Path, Source: "cursor"})
	}
	return out
}
