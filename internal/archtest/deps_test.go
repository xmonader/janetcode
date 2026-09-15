// Package archtest holds tests that pin the direction of dependencies
// between janetcode's packages. Low-level packages must not reach up into
// feature packages: a store or a policy that imports the tool registry
// drags the registry (and everything it imports) into every binary and
// test that only needed the store.
package archtest

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/xmonader/janetcode"

// graph maps each package of the module to its transitive and direct
// module dependencies, as `go list` reports them for non-test builds.
type graph struct {
	deps, imports map[string][]string
}

func load(t *testing.T) graph {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	// One line per package: path, then its transitive deps, then its
	// direct imports, separated by '|'.
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{join .Deps " "}}|{{join .Imports " "}}`, module+"/...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	g := graph{deps: map[string][]string{}, imports: map[string][]string{}}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		if len(parts) != 3 {
			continue
		}
		g.deps[parts[0]] = ours(parts[1])
		g.imports[parts[0]] = ours(parts[2])
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(g.deps) == 0 {
		t.Fatal("go list reported no packages")
	}
	return g
}

func ours(list string) []string {
	var out []string
	for _, p := range strings.Fields(list) {
		if p == module || strings.HasPrefix(p, module+"/") {
			out = append(out, p)
		}
	}
	return out
}

func internal(p string) string { return module + "/internal/" + p }

// expand resolves "llm/..." to llm and every package below it.
func (g graph) expand(t *testing.T, pattern string) []string {
	t.Helper()
	base, tree := strings.CutSuffix(pattern, "/...")
	base = internal(base)
	var out []string
	for p := range g.deps {
		if p == base || (tree && strings.HasPrefix(p, base+"/")) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no package matches %s", pattern)
	}
	return out
}

func contains(list []string, p string) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

func TestDependencyDirection(t *testing.T) {
	g := load(t)
	rules := []struct {
		forbidden string
		packages  []string
	}{
		// tool is the agent's tool registry and its tools. Stores,
		// policies and analyses sit below it and must not use it as a
		// utility bucket.
		{"tool", []string{"session", "perm", "hook", "memory", "repomap", "recipe", "impact", "mutate", "verify", "llm/...", "shell", "weburl", "trust", "xdg"}},
		// feature is the catalogue; it imports its sources, not the
		// other way round.
		{"feature", []string{"proof", "recipe", "slash", "slash/args"}},
		// Providers do not know about the session store.
		{"session", []string{"llm/..."}},
	}
	for _, r := range rules {
		bad := internal(r.forbidden)
		for _, pat := range r.packages {
			for _, p := range g.expand(t, pat) {
				if contains(g.deps[p], bad) {
					t.Errorf("%s depends on %s", p, bad)
				}
			}
		}
	}
}

// TestConfigDoesNotImportTool is narrower than the rules above: config
// still reaches tool transitively through mcp.ServerConfig, because the
// mcp package also holds the adapter that turns a server's tools into
// agent tools. Until that adapter moves out of mcp, only a direct import
// is refused.
func TestConfigDoesNotImportTool(t *testing.T) {
	g := load(t)
	if contains(g.imports[internal("config")], internal("tool")) {
		t.Errorf("config imports %s", internal("tool"))
	}
}

// TestLeaves pins the packages that everything else may import: they
// import nothing from janetcode, so they can never be part of a cycle.
func TestLeaves(t *testing.T) {
	g := load(t)
	for _, pat := range []string{"textfmt", "fsutil", "gitx", "xdg", "slash/args", "weburl", "trust"} {
		for _, p := range g.expand(t, pat) {
			if d := g.deps[p]; len(d) > 0 {
				t.Errorf("%s is meant to be a leaf but depends on %v", p, d)
			}
		}
	}
}
