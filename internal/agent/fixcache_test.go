package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/fixcache"
)

const (
	fixcacheRedOut   = "# m\nm/a.go:3:2: undefined: strings\nFAIL\n[exit code 1]"
	fixcacheGreenOut = "ok  \tm\t0.01s\n[exit code 0]"
)

// fixcacheAgent is an agent with the fix cache on over a workspace
// holding m/a.go.
func fixcacheAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "m", "a.go"), []byte("package m\n\nfunc F() string { return strings.ToUpper(\"x\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{FixCache: true, FixCacheDir: filepath.Join(t.TempDir(), "fixcache")}
	a.Root = root
	return a, root
}

func (a *Agent) fixcacheRun(cmd, out string) string {
	return a.fixCacheBash("bash", flakeArgs(cmd), out, false, func(Event) {})
}

// fixcacheEdit records a change to m/a.go and writes it to disk.
func (a *Agent) fixcacheEdit(t *testing.T, root, after string) {
	t.Helper()
	p := filepath.Join(root, "m", "a.go")
	before, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cp.changes = append(a.cp.changes, Change{Seq: len(a.cp.changes) + 1, Path: p, Existed: true, Before: before, After: []byte(after)})
}

// A red check, an edit, the same check green: the fix is stored, and a
// later session that hits the same error gets it beside the output once.
func TestFixCacheRecordsAndRecalls(t *testing.T) {
	a, root := fixcacheAgent(t)
	if note := a.fixcacheRun("go test ./...", fixcacheRedOut); note != "" {
		t.Fatalf("an empty cache answered: %q", note)
	}
	a.fixcacheEdit(t, root, "package m\n\nimport \"strings\"\n\nfunc F() string { return strings.ToUpper(\"x\") }\n")
	a.fixcacheRun("cd "+root+" && go test ./...", fixcacheGreenOut)
	st, err := fixcache.Open(fixcache.PathFor(a.FixCacheDir, root))
	if err != nil || len(st.Entries) != 1 {
		t.Fatalf("store: %+v %v", st, err)
	}
	e := st.Entries[0]
	if e.Command != "go test ./..." || len(e.Files) != 1 || e.Files[0] != "m/a.go" || !strings.Contains(e.Fix, `+import "strings"`) {
		t.Fatalf("entry = %+v", e)
	}
	if e.Signatures[0] != "m/a.go: undefined: strings" {
		t.Fatalf("signatures = %q", e.Signatures)
	}
	// The same session is not told what it just did.
	if note := a.fixcacheRun("go test ./...", fixcacheRedOut); note != "" {
		t.Fatalf("the recording session was answered: %q", note)
	}
	// A later session in the same repository is.
	b := &Agent{FixCache: true, FixCacheDir: a.FixCacheDir}
	b.Root = root
	note := b.fixcacheRun("go vet ./...", strings.Replace(fixcacheRedOut, "3:2", "9:14", 1))
	if !strings.Contains(note, "seen before in this repository") || !strings.Contains(note, `+import "strings"`) {
		t.Fatalf("note = %q", note)
	}
	if again := b.fixcacheRun("go vet ./...", fixcacheRedOut); again != "" {
		t.Fatalf("the same error was answered twice in one session: %q", again)
	}
	// Another repository does not share the store.
	c, _ := fixcacheAgent(t)
	c.FixCacheDir = a.FixCacheDir
	if note := c.fixcacheRun("go test ./...", fixcacheRedOut); note != "" {
		t.Fatalf("another repository was answered: %q", note)
	}
}

// Nothing is stored for a check that went green with nothing changed, a
// red run whose output names no particular error, or a non-check.
func TestFixCacheStoresOnlyRealFixes(t *testing.T) {
	a, root := fixcacheAgent(t)
	a.fixcacheRun("go test ./...", fixcacheRedOut)
	a.fixcacheRun("go test ./...", fixcacheGreenOut) // green again with no edit: flaky, or fixed elsewhere
	a.fixcacheRun("go test ./...", "FAIL\nexit status 1\n[exit code 1]")
	a.fixcacheEdit(t, root, "package m\n")
	a.fixcacheRun("go test ./...", fixcacheGreenOut)
	a.fixcacheRun("cat m/a.go", "undefined: strings\n[exit code 1]")
	a.fixcacheEdit(t, root, "package m\n\nvar X = 1\n")
	a.fixcacheRun("cat m/a.go", "[exit code 0]")
	if st, _ := fixcache.Open(fixcache.PathFor(a.FixCacheDir, root)); len(st.Entries) != 0 {
		t.Fatalf("stored %+v", st.Entries)
	}
	if _, err := os.Stat(a.FixCacheDir); err == nil {
		t.Fatal("the cache directory was written with nothing to store")
	}
}

func TestFixCacheOff(t *testing.T) {
	a, root := fixcacheAgent(t)
	for _, off := range []func(){
		func() { a.FixCache = false },
		func() { a.FixCache, a.FixCacheDir = true, "" },
		func() { a.FixCacheDir, a.Off = filepath.Join(t.TempDir(), "x"), map[string]bool{"fixcache": true} },
	} {
		off()
		a.fixcacheRun("go test ./...", fixcacheRedOut)
		a.fixcacheEdit(t, root, "package m\n// "+t.Name()+"\n")
		a.fixcacheRun("go test ./...", fixcacheGreenOut)
		if a.FixCacheDir != "" {
			if _, err := os.Stat(a.FixCacheDir); err == nil {
				t.Fatal("wrote while off")
			}
		}
	}
}
