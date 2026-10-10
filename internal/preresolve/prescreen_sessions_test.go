package preresolve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/repomap"
)

// TestPreresolvePrescreenSessions replays recorded sessions' first turns
// against the repository they ran in, as it is now: for each model call
// before the turn's first edit, whether every tool call in it would have
// been answered by the block — a read of a file the block sent whole or
// showed a definition from, or a search for a name it resolved. A call
// answered entirely is one the block could have saved; that share is the
// number the idea's 20% kill line applies to. It is an upper bound (a
// model handed a definition may still read the file around it) over a
// tree that has moved since the session, and sessions whose repository
// is small enough to be inlined whole are left out, as the hook leaves
// them. Skipped unless JANETCODE_PRERESOLVE_PRESCREEN names a sessions
// directory (a copy: the files are only read):
//
//	JANETCODE_PRERESOLVE_PRESCREEN=/tmp/sessions-copy go test ./internal/preresolve -run PrescreenSessions -v
func TestPreresolvePrescreenSessions(t *testing.T) {
	dir := os.Getenv("JANETCODE_PRERESOLVE_PRESCREEN")
	if dir == "" {
		t.Skip("JANETCODE_PRERESOLVE_PRESCREEN not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	indexes := map[string]*repomap.Index{}
	var sessions, named, calls, saved, toolCalls, toolSaved int
	for _, p := range paths {
		first, pre, ok := preresolveFirstTurn(p)
		if !ok || first.cwd == "" {
			continue
		}
		ix, seen := indexes[first.cwd]
		if !seen {
			if st, err := os.Stat(first.cwd); err == nil && st.IsDir() {
				ix, _ = repomap.Build(context.Background(), first.cwd, nil)
			}
			indexes[first.cwd] = ix
		}
		if ix == nil || len(ix.Files) <= 40 {
			continue
		}
		sessions++
		res := Resolve(ix, first.prompt, 0)
		if res.Text != "" {
			named++
		}
		covered := map[string]bool{}
		for f := range res.Whole {
			covered[f] = true
		}
		names := map[string]bool{}
		for _, s := range res.Symbols {
			if i := strings.IndexByte(s, ':'); i > 0 {
				covered[s[:i]] = true
			}
			if i := strings.LastIndexByte(s, ' '); i > 0 {
				n := s[i+1:]
				names[n] = true
				if j := strings.LastIndexByte(n, '.'); j >= 0 {
					names[n[j+1:]] = true
				}
			}
		}
		for _, call := range pre {
			calls++
			all := len(call) > 0
			for _, tc := range call {
				toolCalls++
				if preresolveAnswers(tc, first.cwd, covered, names) {
					toolSaved++
				} else {
					all = false
				}
			}
			if all {
				saved++
			}
		}
		t.Logf("%s: %d call(s) before the first edit, block %d bytes, defs %v, %d whole; prompt %.60q", filepath.Base(p), len(pre), len(res.Text), res.Symbols, len(res.Whole), first.prompt)
	}
	if sessions == 0 {
		t.Skip("no session ran in a repository over the inlining limit that still exists")
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	t.Logf("%d large-repo session(s), %d with something resolved; %d model call(s) before the first edit, %d answered entirely by the block (%.0f%%); %d tool call(s), %d answered (%.0f%%)",
		sessions, named, calls, saved, pct(saved, calls), toolCalls, toolSaved, pct(toolSaved, toolCalls))
}

type preresolveSession struct{ cwd, prompt string }

type preresolveTool struct {
	name string
	in   map[string]any
}

// preresolveFirstTurn reads a session log's first turn: its prompt, and
// the tool calls of each model call before the first edit. A first turn
// with no edit explored before nothing, so it gives no calls.
func preresolveFirstTurn(path string) (preresolveSession, [][]preresolveTool, bool) {
	var s preresolveSession
	data, err := os.ReadFile(path)
	if err != nil {
		return s, nil, false
	}
	var pre [][]preresolveTool
	for _, line := range strings.Split(string(data), "\n") {
		var rec struct {
			Type string `json:"type"`
			Meta struct {
				Cwd string `json:"cwd"`
			} `json:"meta"`
			T       string `json:"t"`
			Turn    int    `json:"turn"`
			Message *struct {
				Blocks []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"blocks"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Type == "meta" {
			s.cwd = rec.Meta.Cwd
			continue
		}
		if rec.Turn > 1 {
			break
		}
		if rec.Message == nil {
			continue
		}
		switch rec.T {
		case "user/message":
			if s.prompt == "" && len(rec.Message.Blocks) > 0 {
				s.prompt = rec.Message.Blocks[0].Text
			}
		case "assistant/message":
			var call []preresolveTool
			for _, b := range rec.Message.Blocks {
				if b.Type != "tool_use" {
					continue
				}
				switch b.Name {
				case "edit_file", "write_file", "notebook_edit", "apply_patch":
					return s, pre, s.prompt != ""
				}
				var in map[string]any
				_ = json.Unmarshal(b.Input, &in)
				call = append(call, preresolveTool{b.Name, in})
			}
			if len(call) > 0 {
				pre = append(pre, call)
			}
		}
	}
	return s, nil, s.prompt != ""
}

// preresolveAnswers is whether the block already holds what one tool call
// went to fetch.
func preresolveAnswers(tc preresolveTool, root string, covered, names map[string]bool) bool {
	str := func(k string) string { v, _ := tc.in[k].(string); return v }
	rel := func(p string) string {
		if filepath.IsAbs(p) {
			if r, err := filepath.Rel(root, p); err == nil {
				p = r
			}
		}
		return filepath.ToSlash(strings.TrimPrefix(p, "./"))
	}
	switch tc.name {
	case "read_file":
		return covered[rel(str("path"))]
	case "grep", "search":
		return names[str("pattern")] || names[str("query")]
	case "repo_map":
		return names[str("symbol")]
	}
	return false
}
