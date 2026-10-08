package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A small tree comes back whole, every text file recorded as read; an
// ignored file and a binary one do not come back with their bytes.
func TestSmallRepoInlinesAndRecordsReads(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"task.md":        "do the thing",
		"pkg/a.py":       "def a():\n    return 1\n",
		"secret.env":     "KEY=hidden-value",
		".aiignore":      "secret.env\n",
		"img.bin":        "\x00\x01\x02",
		"node_modules/x": "dep",
	})
	seen := &Seen{}
	text, n := SmallRepo(context.Background(), nil, root, seen)
	if text == "" {
		t.Fatal("small tree not inlined")
	}
	for _, want := range []string{`<file path="task.md">` + "\ndo the thing\n</file>", `<file path="pkg/a.py">`, "Binary, not shown: img.bin"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	for _, not := range []string{"hidden-value", "dep\n"} {
		if strings.Contains(text, not) {
			t.Errorf("%q leaked into\n%s", not, text)
		}
	}
	abs, _ := resolveOn(nil, root, "pkg/a.py")
	if !seen.Read(abs) {
		t.Error("pkg/a.py not recorded as read")
	}
	if n != 2 { // task.md, pkg/a.py: the walk without git skips dot-files
		t.Errorf("files = %d", n)
	}
}

// Over either limit, nothing is inlined and nothing is recorded.
func TestSmallRepoLimits(t *testing.T) {
	big := t.TempDir()
	writeTree(t, big, map[string]string{"a.txt": strings.Repeat("x", SmallRepoMaxBytes+1)})
	seen := &Seen{}
	if text, _ := SmallRepo(context.Background(), nil, big, seen); text != "" {
		t.Error("a tree over the byte limit was inlined")
	}
	many := t.TempDir()
	files := map[string]string{}
	for i := 0; i <= SmallRepoMaxFiles; i++ {
		files[filepath.Join("d", strings.Repeat("f", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt")] = "x"
	}
	writeTree(t, many, files)
	if text, _ := SmallRepo(context.Background(), nil, many, seen); text != "" {
		t.Error("a tree over the file limit was inlined")
	}
	abs, _ := resolveOn(nil, big, "a.txt")
	if seen.Read(abs) {
		t.Error("a refused tree recorded a read")
	}
}
