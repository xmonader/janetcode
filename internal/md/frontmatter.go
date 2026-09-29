// Package md handles the markdown-with-frontmatter format shared by
// skills, agent profiles and memory entries: a leading "---" block of
// key: value lines, then the body.
package md

import (
	"sort"
	"strings"
)

// Parse splits a leading frontmatter block from the body. Values may be
// quoted; keys are lower-cased. Text without a block comes back whole.
func Parse(text string) (map[string]string, string) {
	meta := map[string]string{}
	text = strings.TrimPrefix(text, "\ufeff")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return meta, text
	}
	rest := text[strings.Index(text, "\n")+1:]
	// Searching from the newline before rest lets an empty block — the
	// closing "---" straight after the opening one, which is what Render
	// writes for meta with nothing in it — close too; it used to be
	// missed, and both delimiters came back as the first lines of the body.
	end := strings.Index("\n"+rest, "\n---") - 1
	if end < -1 {
		return meta, text
	}
	block := ""
	if end >= 0 {
		block = rest[:end]
	}
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		meta[strings.ToLower(strings.TrimSpace(k))] = v
	}
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return meta, body
}

// Render writes a frontmatter block (keys sorted, empty values skipped)
// followed by the body. Values containing a newline or a colon are quoted.
func Render(meta map[string]string, body string) string {
	keys := make([]string, 0, len(meta))
	for k, v := range meta {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, k := range keys {
		v := strings.ReplaceAll(meta[k], "\n", " ")
		if strings.ContainsAny(v, ":#\"'") {
			v = `"` + strings.ReplaceAll(v, `"`, `'`) + `"`
		}
		sb.WriteString(k + ": " + v + "\n")
	}
	sb.WriteString("---\n")
	sb.WriteString(strings.TrimRight(body, "\n"))
	sb.WriteString("\n")
	return sb.String()
}

// List splits a comma or space separated value, tolerating [brackets]
// and quotes, so "a, b", "[a, b]" and "\"a\" \"b\"" all give a and b.
func List(v string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '[' || r == ']' }) {
		if t = strings.Trim(t, "\"'"); t != "" {
			out = append(out, t)
		}
	}
	return out
}
