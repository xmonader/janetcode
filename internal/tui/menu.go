package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/skill"
)

// completions lists slash commands matching the input, or files matching
// the @path being typed at its end.
func (m *model) completions() []command {
	if m.mode().blocked() {
		return nil
	}
	m.comp.args = false
	v := m.input.Value()
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " \n") {
		var exact, fuzzy []command
		for _, c := range m.allCommands() {
			switch {
			case strings.HasPrefix(c.name, v):
				exact = append(exact, c)
			case fuzzyCommand(c.name, v):
				fuzzy = append(fuzzy, c)
			}
		}
		return append(exact, fuzzy...)
	}
	if name, arg, ok := strings.Cut(v, " "); ok && strings.HasPrefix(name, "/") && !strings.Contains(arg, "\n") {
		if c, found := lookup(name); found && c.argsFor != nil {
			// An argument menu offers; it never takes enter, so a typed
			// argument still runs the command the moment you send it.
			m.comp.args = true
			return c.argsFor(m, strings.TrimLeft(arg, " "))
		}
	}
	if word, _, quoted, ok := prompt.MentionAtEnd(v); ok {
		return mentionRows(prompt.CompletePath(m.opts.Cwd, word), quoted)
	}
	return nil
}

// allCommands is the built-in table followed by the user's custom commands
// and the prompts MCP servers offer.
func (m *model) allCommands() []command {
	out := append([]command(nil), commands...)
	names := make([]string, 0, len(m.custom))
	for n := range m.custom {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := m.custom[n]
		out = append(out, command{name: n, args: "[args]", help: sanitize(c.Help) + " · " + shortPath(c.Path)})
	}
	for _, c := range m.skillRows() {
		if m.custom[c.name].Name == "" {
			out = append(out, c)
		}
	}
	if m.opts.MCP != nil && m.opts.MCP.Manager != nil {
		for _, st := range m.opts.MCP.Manager.Status() {
			for _, p := range st.Prompts {
				args := make([]string, 0, len(p.Arguments))
				for _, a := range p.Arguments {
					if a.Required {
						args = append(args, "<"+a.Name+">")
					} else {
						args = append(args, "["+a.Name+"]")
					}
				}
				help := sanitize(strings.TrimSpace(p.Description))
				if help == "" {
					help = "prompt from " + st.Name
				}
				out = append(out, command{name: "/" + mcp.ToolName(st.Name, p.Name), args: strings.Join(args, " "), help: help + " · MCP " + st.Name})
			}
		}
	}
	return out
}

// skillRowsFor is how long the user-invocable skills are kept before
// they are read off disk again.
const skillRowsFor = 2 * time.Second

// skillRowCache is what skillRows read last, and for where.
type skillRowCache struct {
	rows []command
	key  string
	at   time.Time
}

// skillRows is the user-invocable skills as completion rows. Loading
// them reads every skill directory — the project's, the config's and
// .claude's — file by file, and completions() runs several times per
// keystroke (the footer height before the key and after it, the layout,
// the view), so typing a command name used to read every skill file on
// disk four or five times per character. They are cached per working
// directory, which is what /cd changes, and for a couple of seconds, so
// a skill file dropped in mid-session still turns up.
func (m *model) skillRows() []command {
	key := m.opts.Cwd + "\x00" + m.opts.ConfigDir
	if m.skills.key == key && time.Since(m.skills.at) < skillRowsFor {
		return m.skills.rows
	}
	var out []command
	for _, sk := range skill.WithBuiltins(skill.Load(m.opts.Cwd, m.opts.ConfigDir)) {
		if sk.UserInvocable {
			out = append(out, command{name: "/" + sanitize(sk.Name), args: "[args]", help: sanitize(sk.Description) + " · skill"})
		}
	}
	m.skills = skillRowCache{rows: out, key: key, at: time.Now()}
	return out
}

// mentionRows turns completed paths into @mention rows. A path with a
// space in it is offered quoted, @"my notes.md", the form the mention
// parser reads back; offered bare it was inserted and never attached.
// When one row needs quoting (or the user opened a quote) every row is
// quoted, so the rows keep a common prefix a second tab can extend, and
// a directory's quote is left open for the path to go on. A path no
// mention can name is not offered at all.
func mentionRows(paths []string, quoted bool) []command {
	for _, p := range paths {
		if tok, ok := prompt.Mention(p); ok && strings.HasPrefix(tok, `@"`) {
			quoted = true
		}
	}
	var out []command
	for _, p := range paths {
		tok, ok := prompt.Mention(p)
		if !ok {
			continue
		}
		help := "file"
		if strings.HasSuffix(p, "/") {
			help = "directory"
		}
		if quoted {
			tok = `@"` + p
			if help == "file" {
				tok += `"`
			}
		}
		out = append(out, command{name: tok, help: help})
	}
	return out
}

const menuRows = 8

// menu returns the completion rows and the highlighted index, resetting
// the highlight whenever the input changed since it was set.
func (m *model) menu() ([]command, int) {
	c := m.completions()
	if len(c) == 0 {
		return nil, 0
	}
	return c, m.comp.highlight(m.input.Value(), len(c))
}

// moveMenu shifts the highlight; it reports whether a menu was open.
func (m *model) moveMenu(delta int) bool {
	c, idx := m.menu()
	if len(c) == 0 {
		return false
	}
	m.comp.move(m.input.Value(), len(c), delta, idx)
	m.layout()
	m.refresh(false)
	return true
}

// complete applies the highlighted completion to the input. For paths with
// several matches and no explicit choice it extends to the common prefix so
// repeated tabs narrow the list.
func (m *model) complete() bool {
	c, idx := m.menu()
	if len(c) == 0 {
		return false
	}
	// One rule for both menus: with the highlight where it started and
	// several matches, extend to what they all share so a second tab
	// narrows the list; otherwise take the highlighted row.
	v := m.input.Value()
	i := strings.LastIndexAny(v, " \n\t")
	head, word := v[:i+1], v[i+1:]
	// A quoted mention has spaces in it: what is replaced starts at its @.
	if _, start, _, ok := prompt.MentionAtEnd(v); ok && !m.comp.args {
		head, word = v[:start], v[start:]
	}
	// Most argument menus (/open, /resume, /model, /forget, /undo) offer
	// the whole line, command included, not the word being typed: those
	// replace the input rather than its last word. Appending them left
	// "/open /open alpha.txt" after a tab.
	if m.comp.args && strings.HasPrefix(c[idx].name, "/") {
		head, word = "", v
	}
	name := c[idx].name
	if idx == 0 && len(c) > 1 {
		for _, o := range c[1:] {
			name = commonPrefix(name, o.name)
		}
		if len(name) <= len(word) { // nothing to extend: take the highlighted one
			name = c[idx].name
		}
	}
	if !strings.HasSuffix(name, "/") && (len(c) == 1 || name == c[idx].name) {
		name += " "
	}
	m.input.SetValue(head + name)
	m.input.CursorEnd()
	m.comp.pin(m.input.Value())
	return true
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// fuzzyCommand matches a command name against what has been typed so far
// as a subsequence, so "/mcpa" finds /mcp__auth and "/rsm" finds /resume.
// The query is split on "/" first: the separator in a namespaced command
// is part of its shape, not a letter to match.
func fuzzyCommand(name, query string) bool {
	n := strings.ToLower(strings.TrimPrefix(name, "/"))
	for _, part := range strings.Split(strings.ToLower(strings.TrimPrefix(query, "/")), "/") {
		if part == "" {
			continue
		}
		i := 0
		for _, r := range part {
			j := strings.IndexRune(n[i:], r)
			if j < 0 {
				return false
			}
			i += j + 1
		}
	}
	return query != "/"
}
