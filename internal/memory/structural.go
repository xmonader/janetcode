package memory

import (
	"path/filepath"
	"strings"
)

// Structural queries: exact answers from the frontmatter the store
// already keeps (concerns, supersession, validity). No ranking, no
// word overlap — these answer "which", "what replaced", and "what is
// still in force", which a relevance search cannot.

// About returns the open entries whose concerns name one of paths
// exactly. Paths are compared as slash paths after trimming.
func (s *Store) About(paths []string) []Entry {
	have := map[string]bool{}
	for _, p := range paths {
		if p = filepath.ToSlash(strings.TrimSpace(p)); p != "" {
			have[p] = true
		}
	}
	if len(have) == 0 {
		return nil
	}
	var out []Entry
	for _, e := range s.Entries() {
		if e.Inactive() {
			continue
		}
		for _, c := range e.Concerns {
			if have[filepath.ToSlash(strings.TrimSpace(c))] {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// Current returns the entries still in force: open, not superseded.
func (s *Store) Current() []Entry {
	var out []Entry
	for _, e := range s.Entries() {
		if !e.Inactive() {
			out = append(out, e)
		}
	}
	return out
}

// History walks the supersession chain from name forward: the entry,
// then what replaced it, then what replaced that. A name that was never
// stored returns nil. Cycles stop at the first repeat.
func (s *Store) History(name string) []Entry {
	byName := map[string]Entry{}
	for _, e := range s.Entries() {
		byName[e.Name] = e
	}
	var out []Entry
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		e, ok := byName[name]
		if !ok {
			break
		}
		seen[name] = true
		out = append(out, e)
		name = e.SupersededBy
	}
	return out
}
