package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// ToolSearch exposes deferred tools on demand: the model asks for what it
// needs by keyword, gets the matching schemas back, and those tools are
// part of every later request.
type ToolSearch struct {
	Registry *Registry
	// Slim advertises only the fields a headless run uses (see slim.go).
	Slim bool
	// ContextWindow is the model's input limit in tokens, when known. It
	// sets how much of the deferred set may be revealed in one go.
	ContextWindow int

	mu      sync.Mutex
	batched bool
}

// revealShare is the fraction of the context window the one-off batch
// reveal may spend on schemas.
const revealShare = 20 // one twentieth, i.e. 5%

// bytesPerToken is the rough rule the rest of the harness uses too.
const bytesPerToken = 4

const toolSearchMax = 8

func (*ToolSearch) Name() string { return "tool_search" }
func (t *ToolSearch) Description() string {
	// Every tool that can be loaded, loaded already or not: a description
	// that shrank as tools were loaded rewrote the cached tool list on
	// every load.
	if t.Slim {
		return "Find and load a tool that is not listed here (grep, glob, web fetch and search, background jobs, subagents and more) by keyword or exact name."
	}
	names := t.loadableNames()
	if len(names) == 0 {
		return "Find and load tools that are not listed by default. Nothing is deferred right now."
	}
	list := textfmt.ClipBytes(strings.Join(names, ", "), 1500)
	return fmt.Sprintf("Find and load tools that are not listed by default (%d). Search by keyword, or name one exactly to load just that tool; matching tools are returned with their schemas and become callable. Loadable: %s", len(names), list)
}
func (*ToolSearch) Mutating() bool { return false }
func (*ToolSearch) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"query":{"type":"string","description":"Keywords or a tool name; empty lists every deferred tool"}},
"additionalProperties":false}`)
}

func (t *ToolSearch) loadableNames() []string {
	if t.Registry == nil {
		return nil
	}
	return t.Registry.Loadable()
}

func (t *ToolSearch) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if t.Registry == nil {
		return "", fmt.Errorf("tool_search has no registry")
	}
	words := strings.Fields(strings.ToLower(in.Query))
	// A tool that is loaded already is in the list; loading it again
	// would change nothing but the answer's length.
	var loaded []string
	for _, w := range words {
		if t.Registry.Loaded(w) {
			loaded = append(loaded, w)
		}
	}
	if len(loaded) > 0 && len(loaded) == len(words) {
		return fmt.Sprintf("%s: already loaded and callable; its schema is in your tool list.", strings.Join(loaded, ", ")), nil
	}
	defs := t.Registry.Deferred()
	if len(defs) == 0 {
		return "No tools are deferred; everything available is already listed.", nil
	}
	if len(words) == 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d deferred tools (search to load any of them):\n", len(defs))
		for _, d := range defs {
			fmt.Fprintf(&sb, "%s\t%s\n", d.Name, firstSentence(d.Description))
		}
		return textfmt.Truncate(sb.String()), nil
	}
	type hit struct {
		i     int
		score int
	}
	var hits []hit
	for i, d := range defs {
		hay := strings.ToLower(d.Name + " " + d.Description)
		name := strings.ToLower(d.Name)
		score := 0
		for _, w := range words {
			switch {
			case name == w || strings.HasSuffix(name, "__"+w):
				score += 10
			case strings.Contains(name, w):
				score += 4
			case strings.Contains(hay, w):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{i, score})
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No deferred tool matches %q. Call tool_search with an empty query to list them all.", in.Query), nil
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	// A query that names tools exactly loads those and nothing else: every
	// tool loaded rides on every later request, and a word of the name
	// that also appears in other tools' descriptions should not bring
	// them along.
	exact := hits[:0:0]
	for _, h := range hits {
		name := strings.ToLower(defs[h.i].Name)
		for _, w := range words {
			if name == w || strings.HasSuffix(name, "__"+w) {
				exact = append(exact, h)
				break
			}
		}
	}
	if len(exact) > 0 {
		hits = exact
	}
	if len(hits) > toolSearchMax {
		hits = hits[:toolSearchMax]
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Loaded %d tools; they are callable from now on:\n", len(hits))
	chosen := map[string]bool{}
	for _, h := range hits {
		d := defs[h.i]
		t.Registry.Activate(d.Name)
		chosen[d.Name] = true
		fmt.Fprintf(&sb, "\n## %s\n%s\nInput schema: %s\n", d.Name, d.Description, string(d.InputSchema))
	}
	if rest := t.revealRest(defs, chosen); len(rest) > 0 {
		fmt.Fprintf(&sb, "\nAlso loaded, and callable from here on, because they fit alongside: %s\n", strings.Join(rest, ", "))
		sb.WriteString("Call tool_search with one of these names to see its schema.\n")
	}
	return textfmt.Truncate(sb.String()), nil
}

// revealRest activates the rest of the deferred set, once, when their
// schemas together fit a twentieth of the context window.
//
// Every reveal invalidates the cached request prefix, because the tool
// list is part of it: revealing one tool at a time makes the model pay
// for the whole prefix again on each call, which on a large MCP set is
// most of what the session costs. Doing it once, in a batch, breaks the
// prefix once. It is deliberately not repeated: a second batch would be a
// second break, and by then the model has what it asked for.
func (t *ToolSearch) revealRest(defs []llm.ToolDef, already map[string]bool) []string {
	if t.ContextWindow <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.batched {
		return nil
	}
	t.batched = true
	budget := t.ContextWindow / revealShare * bytesPerToken
	var names []string
	for _, d := range defs {
		if already[d.Name] {
			continue
		}
		size := len(d.Name) + len(d.Description) + len(d.InputSchema)
		if size > budget {
			// Keep going: a smaller one further down the list may fit, and
			// the order is by name, not by size.
			continue
		}
		budget -= size
		t.Registry.Activate(d.Name)
		names = append(names, d.Name)
	}
	return names
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 && i < 160 {
		return s[:i+1]
	}
	return textfmt.ClipBytes(s, 160)
}
