package agent

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Conditional instructions. Anything in the system prompt is paid for on
// every request of every session, so what is worth saying about one
// corner of a repository usually goes unsaid. A path-scoped rule is sent
// only when a tool call touches a path it names, and then once for the
// session — the one way to add instructions without adding to the
// prefix. The text arrives as harness text before the next model call,
// not appended to a tool result, so a rule reads as something the
// session was told rather than as output from the tool.

// touchedPaths is what a call is about, as far as a rule is concerned:
// the paths a file tool declares it will write, plus any path named in
// the input. A command's text is not a path and is not matched, so a
// rule cannot be brought in by a word in a shell line.
func touchedPaths(t tool.Tool, input json.RawMessage) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if tc, ok := t.(tool.Toucher); ok {
		if paths, err := tc.Touches(input); err == nil {
			for _, p := range paths {
				add(p)
			}
		}
	}
	var in map[string]any
	if json.Unmarshal(input, &in) == nil {
		if s, ok := in["path"].(string); ok {
			add(s)
		}
		if list, ok := in["paths"].([]any); ok {
			for _, v := range list {
				if s, ok := v.(string); ok {
					add(s)
				}
			}
		}
		if edits, ok := in["edits"].([]any); ok {
			for _, e := range edits {
				if m, ok := e.(map[string]any); ok {
					if s, ok := m["path"].(string); ok {
						add(s)
					}
				}
			}
		}
	}
	return out
}

// applyPathRules asks for the rules a call's paths bring in and queues
// them for the next model call.
func (a *Agent) applyPathRules(t tool.Tool, input json.RawMessage, emit func(Event)) {
	paths := touchedPaths(t, input)
	if len(paths) == 0 {
		return
	}
	if a.PathRules != nil {
		for _, text := range a.PathRules(paths) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			a.queueHarness(text)
			emit(Event{Kind: EvNotice, Text: textfmt.FirstLine(text, 70)})
		}
	}
	if a.PathSkills != nil {
		for _, text := range a.PathSkills(paths) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			a.queueHarness(text)
			emit(Event{Kind: EvNotice, Text: textfmt.FirstLine(text, 70)})
		}
	}
}

// queueHarness holds text for the running turn, delivered as a synthetic
// user block before the next model call (see deliverSteering). Tools run
// in parallel goroutines, so it is guarded like steering is.
func (a *Agent) queueHarness(text string) {
	a.steerMu.Lock()
	a.harness = append(a.harness, text)
	a.steerMu.Unlock()
}
