package repomap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// DefaultBudget is the size of the map injected per turn, in bytes
// (roughly 1500 tokens).
const DefaultBudget = 6000

// TurnPrep returns an Agent.Prepare hook that appends the request's map
// to the user turn. It sits at the turn boundary, so the many model calls
// of the tool loop that follow all share it as cached prefix.
//
// The turn's context is passed in and only bounded here, not replaced:
// building the index walks the tree and asks git what changed, and a
// cancelled turn must take that with it.
func TurnPrep(c *Cache, budget int) func(context.Context, []llm.Block) ([]llm.Block, string) {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return func(ctx context.Context, blocks []llm.Block) ([]llm.Block, string) {
		var text string
		for _, b := range blocks {
			if b.Type == llm.BlockText {
				text += b.Text + "\n"
			}
		}
		if strings.TrimSpace(text) == "" {
			return blocks, ""
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ix, err := c.getBounded(ctx)
		if err != nil || len(ix.Files) < 2 {
			return blocks, ""
		}
		// The index's own root, not the cache's: /cd moves that under the
		// lock at any moment, and the changed files have to be relative
		// to the tree this index was built from anyway.
		ranked := ix.Rank(text, ChangedFiles(ctx, ix.Root))
		m := ix.Render(ranked, budget)
		if m == "" {
			return blocks, ""
		}
		hits := 0
		for _, r := range ranked {
			if len(r.Why) > 0 {
				hits++
			}
		}
		note := fmt.Sprintf("repository map attached: %d files", len(ix.Files))
		if hits > 0 {
			note += fmt.Sprintf(", %d pulled up by the request", hits)
		}
		return append(blocks, llm.Block{Type: llm.BlockText, Text: m, Synthetic: true}), note
	}
}

// Tool exposes the map to the model: a ranking for a query, or the blast
// radius of a symbol or file.
type Tool struct {
	Cache *Cache
}

func (Tool) Name() string { return "repo_map" }
func (Tool) Description() string {
	return "Rank the repository's files for a query (which files and symbols matter for a change), or show the blast radius of a symbol or file: what defines it and every file that uses it. Use it before multi-file changes and refactors instead of grepping around."
}
func (Tool) Mutating() bool { return false }
func (Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"query":{"type":"string","description":"What you are about to change; files and symbols it mentions are pulled up"},
"symbol":{"type":"string","description":"A symbol (Name or Type.Method) or a file path: show what defines and uses it instead of a ranking"},
"transitive":{"type":"boolean","description":"With symbol: also list files that use the direct users"},
"limit":{"type":"integer","description":"Files to list (default 30)"}},
"additionalProperties":false}`)
}

func (t Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query      string `json:"query"`
		Symbol     string `json:"symbol"`
		Transitive bool   `json:"transitive"`
		Limit      int    `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	ix, err := t.Cache.Get(ctx)
	if err != nil {
		return "", err
	}
	if len(ix.Files) == 0 {
		return "", errors.New("no source files found in the repository")
	}
	if in.Limit <= 0 {
		in.Limit = 30
	}
	if in.Symbol != "" {
		r, ok := ix.BlastRadius(in.Symbol, in.Transitive)
		if !ok {
			return "", fmt.Errorf("no symbol or file named %q in the index (%d files)", in.Symbol, len(ix.Files))
		}
		var sb strings.Builder
		if len(r.Sites) > 0 {
			fmt.Fprintf(&sb, "%s is defined at:\n", r.Target)
			for _, s := range r.Sites {
				if s.Line > 0 {
					fmt.Fprintf(&sb, "  %s:%d %s %s\n", s.Path, s.Line, s.Kind, s.Name)
				} else {
					fmt.Fprintf(&sb, "  %s %s %s\n", s.Path, s.Kind, s.Name)
				}
			}
		} else {
			fmt.Fprintf(&sb, "%s is defined in: %s\n", r.Target, strings.Join(r.Definers, ", "))
		}
		if len(r.Uses) > 0 {
			fmt.Fprintf(&sb, "it depends on %d file(s): %s\n", len(r.Uses), strings.Join(r.Uses, ", "))
		}
		if len(r.Referencers) == 0 {
			sb.WriteString("nothing else references it")
		} else {
			fmt.Fprintf(&sb, "used by %d file(s):\n", len(r.Referencers))
			for i, u := range r.Referencers {
				if i == in.Limit {
					fmt.Fprintf(&sb, "  … %d more\n", len(r.Referencers)-i)
					break
				}
				fmt.Fprintf(&sb, "  %s (%.0f)\n", u.Path, u.Weight)
			}
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}
	// ix.Root, not t.Cache.Root: see TurnPrep.
	ranked := ix.Rank(in.Query, ChangedFiles(ctx, ix.Root))
	if len(ranked) > in.Limit {
		ranked = ranked[:in.Limit]
	}
	return ix.Render(ranked, 1<<20), nil
}

// Likely names the files a request most probably concerns, for a
// consulting model that has to look at the code before it advises on it:
// the top of the ranking among the files the request itself pulled up
// (a path, a word or a symbol it mentions, or an uncommitted change),
// else the top n by structure alone. root is the tree the paths are
// relative to. Bounded like TurnPrep; an index that cannot be built in
// time gives nothing rather than a late answer.
func Likely(ctx context.Context, c *Cache, query string, n int) (root string, paths []string) {
	if c == nil || n <= 0 || strings.TrimSpace(query) == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ix, err := c.getBounded(ctx)
	if err != nil || len(ix.Files) == 0 {
		return "", nil
	}
	ranked := ix.Rank(query, ChangedFiles(ctx, ix.Root))
	for _, r := range ranked {
		if len(r.Why) > 0 && len(paths) < n {
			paths = append(paths, r.File.Path)
		}
	}
	if len(paths) == 0 {
		for _, r := range ranked {
			if len(paths) == n {
				break
			}
			paths = append(paths, r.File.Path)
		}
	}
	return ix.Root, paths
}
