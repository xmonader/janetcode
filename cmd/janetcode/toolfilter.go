package main

import (
	"path"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/tool"
)

// toolFilter is --core-tools and --exclude-tools: which tools the model
// is shown at all. Unlike --deny, which refuses a call the model already
// made, a filtered tool is never in a request, so it costs no tokens and
// invites no attempts. Names may be globs (mcp__github__*).
type toolFilter struct {
	core    []string
	exclude []string
}

func parseToolFilter(core, exclude string) (toolFilter, error) {
	f := toolFilter{core: splitNames(core), exclude: splitNames(exclude)}
	for _, p := range append(append([]string{}, f.core...), f.exclude...) {
		if _, err := path.Match(p, ""); err != nil {
			return toolFilter{}, headless.BadInput("bad tool pattern %q: %v", p, err)
		}
	}
	return f, nil
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (f toolFilter) active() bool { return len(f.core) > 0 || len(f.exclude) > 0 }

// gate is the tool.Registry gate: "" admits name.
func (f toolFilter) gate(name string) string {
	if matchAny(f.exclude, name) {
		return "excluded by --exclude-tools"
	}
	if len(f.core) > 0 && !matchAny(f.core, name) {
		return "not in --core-tools"
	}
	return ""
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// activate shows the model, from the first request, the deferred tools
// --core-tools names: they would otherwise wait for a tool_search the
// same list usually leaves out.
func (f toolFilter) activate(r *tool.Registry) {
	for _, d := range r.Deferred() {
		if matchAny(f.core, d.Name) {
			r.Activate(d.Name)
		}
	}
}

// check refuses a plain name that is no tool of this session: a typo in
// --exclude-tools would otherwise leave the tool it meant to remove in
// place without a word. Globs and MCP names are let through, since MCP
// servers connect after this runs.
func (f toolFilter) check(r *tool.Registry) error {
	var unknown []string
	for _, n := range append(append([]string{}, f.core...), f.exclude...) {
		if strings.ContainsAny(n, "*?[") || strings.HasPrefix(n, "mcp__") {
			continue
		}
		if _, ok := r.Get(n); ok {
			continue
		}
		if _, ok := r.Withheld(n); ok {
			continue
		}
		unknown = append(unknown, n)
	}
	if len(unknown) == 0 {
		return nil
	}
	known := r.Names()
	sort.Strings(known)
	return headless.BadInput("no tool named %s in this session (tools: %s)", strings.Join(unknown, ", "), strings.Join(known, ", "))
}
