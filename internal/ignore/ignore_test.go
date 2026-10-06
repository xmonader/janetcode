package ignore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func put(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitignoreSemantics(t *testing.T) {
	root := t.TempDir()
	put(t, root, ".aiignore", `# secrets and data
.env*
!.env.example
/private/
data/**/*.csv
build/
*.pem
\#literal
`)
	put(t, root, "sub/.agentignore", "notes.md\n/local.txt\n")
	put(t, root, "sub/.janetcodeignore", "!notes.md\n")
	m := For(root)
	cases := []struct {
		path  string
		dir   bool
		want  bool
		label string
	}{
		{".env", false, true, "basename glob"},
		{"deep/.env.prod", false, true, "basename at depth"},
		{".env.example", false, false, "negation re-includes"},
		{"private", true, true, "anchored dir"},
		{"private/a/b.go", false, true, "under excluded dir"},
		{"x/private/a.go", false, false, "anchored only at root"},
		{"data/2024/q1/x.csv", false, true, "double star"},
		{"data/x.csv", false, true, "double star matches zero dirs"},
		{"data/x.json", false, false, "other extension"},
		{"build", false, false, "dir-only pattern skips a file"},
		{"a/build/out.o", false, true, "dir-only at depth"},
		{"keys/server.pem", false, true, "extension"},
		{"#literal", false, true, "escaped hash"},
		{"sub/notes.md", false, false, "later file in same dir re-includes"},
		{"sub/local.txt", false, true, "nested anchored"},
		{"local.txt", false, false, "nested rule stays in its subtree"},
		{"main.go", false, false, "plain file"},
	}
	for _, c := range cases {
		got, src := m.Excluded(filepath.Join(root, c.path), c.dir)
		if got != c.want {
			t.Errorf("%s (%s): excluded = %v (%s), want %v", c.path, c.label, got, src, c.want)
		}
	}
	if _, src := m.Excluded(filepath.Join(root, "private/x"), false); src != ".aiignore:4" {
		t.Errorf("src = %q", src)
	}
}

// Editing the ignore file takes effect without a restart.
func TestRulesReloadOnChange(t *testing.T) {
	root := t.TempDir()
	put(t, root, ".aiignore", "a.txt\n")
	m := For(root)
	if hit, _ := m.Excluded(filepath.Join(root, "b.txt"), false); hit {
		t.Fatal("b.txt excluded early")
	}
	put(t, root, ".aiignore", "a.txt\nb.txt\n")
	time.Sleep(fresh + 50*time.Millisecond)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(root, ".aiignore"), future, future)
	if hit, _ := m.Excluded(filepath.Join(root, "b.txt"), false); !hit {
		t.Fatal("change not picked up")
	}
}

func TestNoIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	if hit, _ := For(root).Excluded(filepath.Join(root, "x"), false); hit {
		t.Fatal("excluded with no rules")
	}
	if hit, _ := For(root).Excluded(filepath.Join(filepath.Dir(root), "outside"), false); hit {
		t.Fatal("path outside root excluded")
	}
}
