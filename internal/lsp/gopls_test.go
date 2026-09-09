package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoplsRealDiagnostics runs the real server when it is installed.
func TestGoplsRealDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F() int { return \"no\" }\n"), 0o644)
	m := New(root, Config{Timeout: 60})
	defer m.Close()
	lines, ok, err := m.Diagnose(context.Background(), "m.go")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v lines=%q", ok, err, lines)
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "m.go:3:") || !strings.Contains(lines[0], "error:") {
		t.Fatalf("lines = %q", lines)
	}
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F() int { return 1 }\n"), 0o644)
	if lines, ok, err := m.Diagnose(context.Background(), "m.go"); err != nil || !ok || len(lines) != 0 {
		t.Fatalf("after fix: ok=%v err=%v lines=%q", ok, err, lines)
	}
}

// TestGoplsSymbolQueries exercises the query surface against the real
// server: what grep cannot do — telling a declaration from a mention, and
// finding every use of one package's name and not another's.
func TestGoplsSymbolQueries(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("cfg/cfg.go", `package cfg

// Config is the app configuration.
type Config struct{ Name string }

// Load reads a Config.
func Load() *Config { return &Config{Name: "x"} }
`)
	write("main.go", `package main

import "example.com/m/cfg"

// Config here is a different type with the same name.
type Config struct{ Other int }

func main() {
	c := cfg.Load()
	_ = c.Name
	_ = Config{Other: 1}
}
`)
	m := New(root, Config2Timeout())
	defer m.Close()

	// "Where is Load defined?" — named, the way an agent asks it. The only
	// mention in main.go is the call, and it resolves into the package.
	defs, err := m.Definition(context.Background(), "main.go", Point{Symbol: "Load"})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(defs) == 0 || !strings.HasPrefix(defs[0].Path, "cfg/") {
		t.Fatalf("cfg.Load should resolve into cfg/: %+v", defs)
	}
	// Naming a line points at its first identifier, so line 9 is c itself.
	if own, err := m.Definition(context.Background(), "main.go", Point{Line: 9}); err != nil || len(own) == 0 || own[0].Path != "main.go" {
		t.Fatalf("line 9 should resolve to c in main.go: %+v %v", own, err)
	}

	refs, err := m.References(context.Background(), "cfg/cfg.go", Point{Symbol: "Config"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	// Every reference is to cfg.Config; main.go's own Config must not be
	// among them, which is exactly the distinction grep cannot make.
	if len(refs) < 2 {
		t.Fatalf("references = %+v", refs)
	}
	for _, r := range refs {
		if r.Path == "main.go" && strings.Contains(r.Text, "type Config struct") {
			t.Fatalf("matched the unrelated Config in main.go: %+v", r)
		}
	}

	hov, err := m.Hover(context.Background(), "cfg/cfg.go", Point{Symbol: "Load"})
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if !strings.Contains(hov, "Load") || !strings.Contains(hov, "Config") {
		t.Fatalf("hover = %q", hov)
	}

	syms, err := m.Outline(context.Background(), "cfg/cfg.go")
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	if !contains(names, "Config") || !contains(names, "Load") {
		t.Fatalf("outline = %v", names)
	}

	// The tool renders what the model sees.
	out, err := SymbolTool{M: m}.Run(context.Background(), []byte(`{"action":"definition","path":"main.go","symbol":"Load"}`))
	if err != nil || !strings.Contains(out, "cfg/cfg.go") {
		t.Fatalf("tool definition: %v %q", err, out)
	}
	out, err = SymbolTool{M: m}.Run(context.Background(), []byte(`{"action":"outline","path":"cfg/cfg.go"}`))
	if err != nil || !strings.Contains(out, "Load") {
		t.Fatalf("tool outline: %v %q", err, out)
	}
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}

// Config2Timeout is the generous timeout a cold gopls needs to index.
func Config2Timeout() Config { return Config{Timeout: 60} }
