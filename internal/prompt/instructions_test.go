package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repository root says something about itself, a package three levels
// down says something more particular, and the model should read both —
// the nearest last, since it is the most specific.
func TestInstructionsWalkUpToTheRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "JANETCODE.md"), "root rules\n")
	writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "sub rules\n")
	deep := filepath.Join(root, "sub", "deeper")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got := Instructions(deep)
	if len(got) != 2 {
		t.Fatalf("instructions = %+v", got)
	}
	if got[0].Text != "root rules" || got[1].Text != "sub rules" {
		t.Fatalf("order = %+v", got)
	}
	if got[1].Name != "sub/AGENTS.md" {
		t.Fatalf("name = %q, want a path relative to the root", got[1].Name)
	}
}

// Without a repository there is no boundary to walk up to, so only the
// working directory is read: walking to the filesystem root would read
// whatever a stranger left in a home directory.
func TestInstructionsWithoutARepositoryStayPut(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "JANETCODE.md"), "outer\n")
	inner := filepath.Join(base, "inner")
	writeFile(t, filepath.Join(inner, "JANETCODE.md"), "inner\n")
	got := Instructions(inner)
	if len(got) != 1 || got[0].Text != "inner" {
		t.Fatalf("instructions = %+v", got)
	}
}

func TestImportsAreExpanded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "JANETCODE.md"), "before\n@docs/style.md\nafter\n")
	writeFile(t, filepath.Join(root, "docs", "style.md"), "the style\n@deeper.md\n")
	writeFile(t, filepath.Join(root, "docs", "deeper.md"), "and more\n")
	got := Instructions(root)
	if len(got) != 1 {
		t.Fatalf("instructions = %+v", got)
	}
	text := got[0].Text
	for _, want := range []string{"before", "the style", "and more", "after"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q missing from %q", want, text)
		}
	}
}

func TestImportsAreBounded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	// A cycle stops rather than recursing forever.
	writeFile(t, filepath.Join(root, "JANETCODE.md"), "@a.md\n")
	writeFile(t, filepath.Join(root, "a.md"), "in a\n@b.md\n")
	writeFile(t, filepath.Join(root, "b.md"), "in b\n@a.md\n")
	text := Instructions(root)[0].Text
	if !strings.Contains(text, "in a") || !strings.Contains(text, "in b") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "already included") {
		t.Fatalf("the cycle was not reported: %q", text)
	}

	// A chain deeper than the cap stops at it.
	deep := t.TempDir()
	writeFile(t, filepath.Join(deep, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(deep, "JANETCODE.md"), "@l1.md\n")
	for i := 1; i <= 8; i++ {
		body := "level\n@l" + string(rune('0'+i+1)) + ".md\n"
		writeFile(t, filepath.Join(deep, "l"+string(rune('0'+i))+".md"), body)
	}
	text = Instructions(deep)[0].Text
	if !strings.Contains(text, "only followed") {
		t.Fatalf("the depth cap did not stop the chain: %q", text)
	}

	// Outside the project is outside, whatever the path says.
	out := t.TempDir()
	writeFile(t, filepath.Join(out, "secret.md"), "not for the model\n")
	esc := t.TempDir()
	writeFile(t, filepath.Join(esc, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(esc, "JANETCODE.md"), "@"+filepath.Join(out, "secret.md")+"\n")
	text = Instructions(esc)[0].Text
	if strings.Contains(text, "not for the model") {
		t.Fatal("an import escaped the project root")
	}
	if !strings.Contains(text, "outside the project") {
		t.Fatalf("text = %q", text)
	}
}

// A line shaped like an import that names no file in the project (a
// handle, a package name, a directory) reads as the author wrote it,
// not as an error message about a file that was never meant.
func TestAnImportOfNoFileIsLeftAsWritten(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "docs", "a.md"), "x\n")
	writeFile(t, filepath.Join(root, "JANETCODE.md"), "Owners:\n  @alice\nNeeds:\n@types/node\n@docs\n")
	text := Instructions(root)[0].Text
	if strings.Contains(text, "skipped") || !strings.Contains(text, "\n  @alice\n") || !strings.Contains(text, "@types/node\n@docs") {
		t.Fatalf("text = %q", text)
	}
}

func TestOnlyAWholeLineIsAnImport(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "other.md"), "imported\n")
	writeFile(t, filepath.Join(root, "JANETCODE.md"),
		"mail me at me@example.com\nsee @other.md for more\n```\n@other.md\n```\n")
	text := Instructions(root)[0].Text
	if strings.Contains(text, "imported") {
		t.Fatalf("prose or a fenced example was read as an import: %q", text)
	}
}
