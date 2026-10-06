package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestToolFilterGate(t *testing.T) {
	f, err := parseToolFilter("read_file, grep ,mcp__github__*,bash", "bash")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"read_file":            "",
		"mcp__github__open_pr": "",
		"bash":                 "excluded by --exclude-tools",
		"edit_file":            "not in --core-tools",
		"mcp__slack__post":     "not in --core-tools",
	} {
		if got := f.gate(name); got != want {
			t.Errorf("gate(%s) = %q, want %q", name, got, want)
		}
	}
	if _, err := parseToolFilter("[", ""); !errors.Is(err, headless.ErrBadInput) {
		t.Fatalf("bad glob: %v", err)
	}
}

// A misspelt name is refused rather than silently matching nothing.
func TestToolFilterRefusesUnknownNames(t *testing.T) {
	r := tool.NewRegistry(tool.ReadFile{}, tool.Bash{})
	f, _ := parseToolFilter("", "bahs,mcp__later__x,ba*")
	r.Gate(f.gate)
	err := f.check(r)
	if !errors.Is(err, headless.ErrBadInput) || !strings.Contains(err.Error(), "no tool named bahs") || strings.Contains(err.Error(), "mcp__later") {
		t.Fatalf("err = %v", err)
	}
	f, _ = parseToolFilter("", "bash")
	r = tool.NewRegistry(tool.ReadFile{}, tool.Bash{})
	r.Gate(f.gate)
	if err := f.check(r); err != nil {
		t.Fatalf("withheld bash counted as unknown: %v", err)
	}
	if _, ok := r.Get("bash"); ok {
		t.Fatal("bash still registered")
	}
}

// A deferred tool --core-tools names is shown from the start: the
// tool_search that would reveal it is usually not on the list.
func TestCoreToolsActivatesDeferred(t *testing.T) {
	r := tool.NewRegistry(tool.ReadFile{}, tool.Bash{})
	r.AddDeferred(tool.Computer{})
	f, _ := parseToolFilter("read_file,comp*", "")
	r.Gate(f.gate)
	f.activate(r)
	var names []string
	for _, d := range r.Defs() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "computer,read_file" && strings.Join(names, ",") != "read_file,computer" {
		t.Fatalf("defs = %v", names)
	}
}
