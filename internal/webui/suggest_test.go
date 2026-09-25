package webui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestionsComeFromTheProject(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "parser", "parse.go"), "package parser\n\n// TODO lists are what Parse reads.\n// TODO: handle quoted keys\nfunc Parse() {}\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	// Larger, untested, but a helper for tests: never suggested.
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		writeFile(t, filepath.Join(dir, "internal", "fstest", f), "package fstest\n")
	}
	writeFile(t, filepath.Join(dir, "main_test.go"), "package main\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-qm", "feat: parse ini files")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() { println() }\n")

	got := projectSuggestions(context.Background(), dir)
	if len(got) != maxSuggestions {
		t.Fatalf("got %d suggestions: %+v", len(got), got)
	}
	want := []struct{ label, prompt string }{
		{"Review my 1 changed file", "uncommitted changes"},
		{"Resolve a TODO in parse.go", "parser/parse.go:4 (// TODO: handle quoted keys)"},
		{"Check the last commit", `"feat: parse ini files"`},
		{"Add tests to parser", "parser has no tests"},
	}
	for i, w := range want {
		if got[i].Label != w.label || !strings.Contains(got[i].Prompt, w.prompt) {
			t.Errorf("suggestion %d = %+v, want label %q and a prompt with %q", i, got[i], w.label, w.prompt)
		}
	}
}

func TestOutsideARepositoryTheSuggestionsAreGeneral(t *testing.T) {
	got := projectSuggestions(context.Background(), t.TempDir())
	if len(got) != maxSuggestions {
		t.Fatalf("got %+v", got)
	}
	for i, g := range got {
		if g != generalSuggestions[i] {
			t.Fatalf("suggestion %d = %+v, want %+v", i, g, generalSuggestions[i])
		}
	}
}
