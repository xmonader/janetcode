// Package manual is the janetcode manual, compiled into the binary.
//
// The Go file lives beside the Markdown rather than the other way round
// because go:embed cannot reach out of its own directory, and a copy of
// the manual under internal/ would be a second manual to keep in step.
// The pages are the source of truth; this file only makes them
// searchable offline, so /help <topic> and the introspect tool can quote
// the manual without a network call or a model call.
package manual

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// Section is one heading of one page, with the prose under it.
type Section struct {
	Page    string // file name without .md, e.g. "interface"
	Title   string // heading text, or the page title for the preamble
	Level   int    // 1 for the page title, 2 for ##, and so on
	Body    string // the text under the heading, without the heading
	Anchor  string // "interface#verification-gates"
	Snippet string // the first paragraph, for a listing
}

var (
	loaded   []Section
	loadOnce bool
)

// Sections returns every section of every page, in page then document
// order. It parses on first use: nothing is done at startup, so a run
// that never asks for help pays nothing for the manual but its bytes.
func Sections() []Section {
	if loadOnce {
		return loaded
	}
	loadOnce = true
	names, _ := files.ReadDir(".")
	var pages []string
	for _, n := range names {
		if strings.HasSuffix(n.Name(), ".md") {
			pages = append(pages, n.Name())
		}
	}
	sort.Strings(pages)
	for _, p := range pages {
		data, err := files.ReadFile(p)
		if err != nil {
			continue
		}
		loaded = append(loaded, parse(strings.TrimSuffix(p, ".md"), string(data))...)
	}
	return loaded
}

// Pages names the manual pages.
func Pages() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range Sections() {
		if !seen[s.Page] {
			seen[s.Page] = true
			out = append(out, s.Page)
		}
	}
	return out
}

// Page returns one page whole.
func Page(name string) (string, bool) {
	data, err := files.ReadFile(strings.TrimSuffix(name, ".md") + ".md")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// parse splits a page into sections on its headings. Fenced code is
// skipped, because a shell transcript in a fence is full of lines that
// begin with a hash and none of them is a heading.
func parse(page, text string) []Section {
	var out []Section
	cur := Section{Page: page, Level: 1, Title: page}
	var body strings.Builder
	fence := false
	flush := func() {
		cur.Body = strings.TrimSpace(body.String())
		cur.Anchor = page
		if cur.Level > 1 {
			cur.Anchor = page + "#" + slug(cur.Title)
		}
		cur.Snippet = firstParagraph(cur.Body)
		if cur.Body != "" || cur.Level > 1 {
			out = append(out, cur)
		}
		body.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
		}
		if !fence {
			if lvl, title, ok := heading(line); ok {
				if lvl == 1 && len(out) == 0 && body.Len() == 0 {
					cur.Title = title
					continue
				}
				flush()
				cur = Section{Page: page, Level: lvl, Title: title}
				continue
			}
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return out
}

func heading(line string) (level int, title string, ok bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return 0, "", false
	}
	return n, strings.TrimSpace(line[n+1:]), true
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstParagraph(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.Index(body, "\n\n"); i > 0 {
		body = body[:i]
	}
	return strings.Join(strings.Fields(body), " ")
}

// Find ranks sections against a query and returns the best matches. It
// is a word search, not an index: the manual is some tens of kilobytes
// and a linear pass over it costs microseconds, which is cheaper than
// carrying an index that can go stale.
func Find(query string, limit int) []Section {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	type scored struct {
		s Section
		n int
	}
	var hits []scored
	for _, s := range Sections() {
		title := strings.ToLower(s.Title)
		body := strings.ToLower(s.Body)
		n := 0
		for _, w := range words {
			w = strings.TrimPrefix(w, "/")
			if len(w) < 2 {
				continue
			}
			switch {
			case title == w:
				n += 200
			case strings.Contains(title, w):
				n += 80
			}
			if c := strings.Count(body, w); c > 0 {
				n += 10 + min(c, 5)
			}
		}
		if n > 0 {
			hits = append(hits, scored{s, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Section, len(hits))
	for i, h := range hits {
		out[i] = h.s
	}
	return out
}

// Excerpt renders a section trimmed to at most maxBytes, cut at a line
// boundary so a quotation never ends mid-word.
func (s Section) Excerpt(maxBytes int) string {
	head := "docs/manual/" + s.Page + ".md · " + s.Title + "\n"
	body := s.Body
	if maxBytes > 0 && len(body) > maxBytes {
		body = body[:maxBytes]
		if i := strings.LastIndexByte(body, '\n'); i > maxBytes/2 {
			body = body[:i]
		}
		body = strings.TrimRight(body, "\n") + "\n…"
	}
	return head + body
}
