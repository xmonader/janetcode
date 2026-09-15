// Searching saved sessions: what /search reads. The store itself is in
// session.go; nothing here writes.
package session

import (
	"errors"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
)

// Hit is one search match in a saved session.
type Hit struct {
	Info    Info
	Role    llm.Role
	Snippet string
	Matches int
}

// Search scans every session (all directories when cwd is "") for
// messages containing every word of the query, case-insensitively, and
// returns the best snippet per session, newest first.
func Search(dir, cwd, query string, limit int) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		s, msgs, err := Open(dir, strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil || (cwd != "" && s.Meta.Cwd != cwd) {
			continue
		}
		var best Hit
		for _, m := range msgs {
			for _, b := range m.Blocks {
				if b.Type != llm.BlockText && b.Type != llm.BlockToolResult {
					continue
				}
				low := strings.ToLower(b.Text)
				n := 0
				first := -1
				for _, w := range words {
					i := strings.Index(low, w)
					if i < 0 {
						n = 0
						break
					}
					n++
					if first < 0 || i < first {
						first = i
					}
				}
				if n == 0 {
					continue
				}
				score := n
				if b.Type == llm.BlockText {
					score++ // conversation beats tool output
				}
				if score > best.Matches {
					best = Hit{Role: m.Role, Snippet: snippetAround(b.Text, first), Matches: score}
				}
			}
		}
		if best.Matches == 0 {
			continue
		}
		best.Info = Info{Meta: s.Meta, Messages: len(msgs)}
		hits = append(hits, best)
	}
	sort.Slice(hits, func(i, j int) bool { return newer(hits[i].Info.Meta, hits[j].Info.Meta) })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// snippetAround returns the line of text around byte offset at, which the
// caller found in a lowercased copy of it.
//
// Lowercasing is not length-preserving: Ⱥ and Ⱦ each gain a byte, so a
// message containing one puts the offset past the end of the original and
// every slice below panics. The offset is therefore clamped and pulled
// back to a rune boundary before anything is cut with it — a snippet a
// couple of bytes off centre beats taking the whole search down.
func snippetAround(text string, at int) string {
	if at > len(text) {
		at = len(text)
	}
	if at < 0 {
		at = 0
	}
	for at > 0 && at < len(text) && !utf8.RuneStart(text[at]) {
		at--
	}
	start := strings.LastIndexByte(text[:at], '\n') + 1
	end := strings.IndexByte(text[at:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += at
	}
	line := strings.TrimSpace(text[start:end])
	if len(line) > 120 {
		from := at - start - 40
		if from < 0 {
			from = 0
		}
		if from+120 > len(line) {
			from = len(line) - 120
		}
		to := from + 120
		for from > 0 && !utf8.RuneStart(line[from]) {
			from--
		}
		for to < len(line) && !utf8.RuneStart(line[to]) {
			to++
		}
		line = "…" + line[from:to] + "…"
	}
	return line
}
