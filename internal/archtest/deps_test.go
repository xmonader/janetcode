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
		{"tool", []string{"session", "perm", "hook", "memory", "repomap", "recipe", "impact", "mutate", "verify", "llm/...", "shell", "weburl", "trust", "xdg",
			// config holds MCP server settings; the adapter that turns a
			// server's tools into agent tools lives in mcp/mcptool, so
			// neither config nor the mcp client needs the registry.
			"config", "mcp"}},
		// feature is the catalogue; it imports its sources, not the
		// other way round.
		{"feature", []string{"proof", "recipe", "slash", "slash/args"}},
		// Providers do not know about the session store.
		{"session", []string{"llm/..."}},
		// The agent loop takes plain values and small interfaces from the
		// app layer (settingsapply, verify.MutateFunc); it does not reach
		// the config file, the servers and stores behind it, or the
		// mutation runner.
		{"config", []string{"agent", "verify"}},
		{"lsp", []string{"agent"}},
		{"mcp", []string{"agent"}},
		{"theme", []string{"agent"}},
		{"memory", []string{"agent"}},
		{"skill", []string{"agent"}},
		{"mutate", []string{"agent", "verify"}},
		{"worktree", []string{"agent"}},
		// The feature packages phase 3 lifted out of internal/agent.
		// Each of them is imported BY the agent loop, so none of them
		// may import it back: accept owns the /accept state and the
		// lock over the approved tests, undoabout decides which recorded
		// changes a description means, recipe/run replays a recipe's
		// steps through a Dispatcher the agent implements, cron is the
		// session scheduler, critic is the trajectory model and /why.
		// A feature that reaches back into the agent stops being a
		// feature and becomes another part of the loop.
		{"agent", []string{"accept", "undoabout", "recipe/...", "cron", "critic"}},
		// The two types those features share with the agent are data,
		// not behaviour: change is the provenance record and its diff
		// arithmetic, terms the BM25 splitting two rankings agree on.
		// Either one importing a feature package would put the shared
		// bottom of the graph above the things built on it.
		{"accept", []string{"change", "terms"}},
		{"undoabout", []string{"change", "terms"}},
		{"critic", []string{"change", "terms"}},
		// undoabout decides what to undo; applying it means checkpoints,
		// which the agent owns. It must not grow its own way to write.
		{"snapshot", []string{"undoabout"}},
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

// TestLeaves pins the packages that everything else may import: they
// import nothing from janetcode, so they can never be part of a cycle.
func TestLeaves(t *testing.T) {
	g := load(t)
	for _, pat := range []string{"textfmt", "fsutil", "gitx", "xdg", "slash/args", "weburl", "trust", "terms", "jev"} {
		for _, p := range g.expand(t, pat) {
			if d := g.deps[p]; len(d) > 0 {
				t.Errorf("%s is meant to be a leaf but depends on %v", p, d)
			}
		}
	}
}
