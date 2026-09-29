package tool

import (
	"strings"
	"testing"
)

// Disable takes a tool out now and keeps one of its name from being
// registered later, as an MCP server's tools are.
func TestRegistryDisable(t *testing.T) {
	r := NewRegistry(ReadFile{}, WebSearch{})
	r.Disable(func(n string) bool { return n == "web_search" || strings.HasPrefix(n, "mcp__fs__") }, "disabled by policy")
	if _, ok := r.Get("web_search"); ok {
		t.Fatal("web_search still registered")
	}
	if _, ok := r.Get("read_file"); !ok {
		t.Fatal("read_file was removed")
	}
	r.Add(WebSearch{})
	if _, ok := r.Get("web_search"); ok {
		t.Fatal("web_search re-registered after Disable")
	}
	if why, ok := r.Withheld("web_search"); !ok || why != "disabled by policy" {
		t.Errorf("Withheld = %q %v", why, ok)
	}
	for _, d := range r.Defs() {
		if d.Name == "web_search" {
			t.Fatal("web_search offered to the model")
		}
	}
}

func TestWebSearchHost(t *testing.T) {
	if h := (WebSearch{}).Host(); h != "html.duckduckgo.com" {
		t.Errorf("default host = %q", h)
	}
	w := WebSearch{}
	w.Config.Provider = "brave"
	if h := w.Host(); h != "api.search.brave.com" {
		t.Errorf("brave host = %q", h)
	}
}
