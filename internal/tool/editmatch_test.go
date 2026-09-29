package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editIn(path, old, new string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"path": path, "old_string": old, "new_string": new})
	return b
}

func TestEditFoldsEquivalentCharacters(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.md")
	// The file has plain ASCII; the model quotes it with typographic
	// characters, which is what a model that has "read" prose does.
	os.WriteFile(p, []byte("title - \"one\" and 'two'\nbody\n"), 0o644)
	e := EditFile{Root: root}
	out, err := e.Run(context.Background(), editIn("a.md", "title — “one” and ‘two’", "title"))
	if err != nil {
		t.Fatalf("fold rung did not match: %v", err)
	}
	if !strings.Contains(out, "folding equivalent characters") {
		t.Fatalf("the result should say the text was folded: %q", out)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "title\nbody\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditFoldsNonBreakingSpaceInIndent(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.py")
	os.WriteFile(p, []byte("def f():\n    return 1\n"), 0o644)
	e := EditFile{Root: root}
	// A non-breaking space in the indent the model quoted.
	old := "    return 1"
	if _, err := e.Run(context.Background(), editIn("a.py", old, "    return 2")); err != nil {
		t.Fatalf("fold rung did not match a non-breaking indent: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "def f():\n    return 2\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditToleratesTrailingWhitespaceButNotLeading(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("func f() {\n\treturn 1   \n}\n"), 0o644)
	e := EditFile{Root: root}
	// The file has trailing spaces the model did not quote.
	out, err := e.Run(context.Background(), editIn("a.go", "\treturn 1\n", "\treturn 2\n"))
	if err != nil {
		t.Fatalf("trailing-whitespace rung did not match: %v", err)
	}
	if !strings.Contains(out, "trailing whitespace") {
		t.Fatalf("the result should say what was relaxed: %q", out)
	}
	if data, _ := os.ReadFile(p); string(data) != "func f() {\n\treturn 2\n}\n" {
		t.Fatalf("file = %q", data)
	}

	// Leading whitespace is never relaxed: in Python it is the program.
	os.WriteFile(p, []byte("def f():\n    return 1\n"), 0o644)
	if _, err := e.Run(context.Background(), editIn("a.go", "\n  return 1\n", "\n  return 2\n")); err == nil {
		t.Fatal("an edit with the wrong indent was accepted")
	}
}

func TestEditReportsTheClosestRegionOnAMiss(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package a\n\nfunc Parse(s string) (int, error) {\n\tn := len(s)\n\treturn n, nil\n}\n"), 0o644)
	e := EditFile{Root: root}
	_, err := e.Run(context.Background(), editIn("a.go",
		"func Parse(s string) (int, error) {\n\tn := len(s)\n\treturn n, errors.New(\"x\")\n}",
		"func Parse(s string) int { return 0 }"))
	if err == nil {
		t.Fatal("the edit should not have matched")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string not found") {
		t.Fatalf("err = %v", msg)
	}
	if !strings.Contains(msg, "closest thing in the file") || !strings.Contains(msg, "return n, nil") {
		t.Fatalf("the miss should show the closest region as a diff: %v", msg)
	}
}

func TestNoNearMissWhenNothingIsClose(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package a\n"), 0o644)
	e := EditFile{Root: root}
	_, err := e.Run(context.Background(), editIn("a.go", "the quick brown fox", "x"))
	if err == nil {
		t.Fatal("expected a miss")
	}
	if strings.Contains(err.Error(), "closest thing") {
		t.Fatalf("nothing in the file is close; the report is noise: %v", err)
	}
}

func TestExactMatchStillWinsAndIsUnannotated(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("a - b\n"), 0o644)
	e := EditFile{Root: root}
	out, err := e.Run(context.Background(), editIn("a.go", "a - b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "folding") || strings.Contains(out, "trailing whitespace") {
		t.Fatalf("an exact match must not be annotated: %q", out)
	}
}

func TestFoldedAmbiguityIsRefused(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.md")
	os.WriteFile(p, []byte("x - y\nx - y\n"), 0o644)
	e := EditFile{Root: root}
	_, err := e.Run(context.Background(), editIn("a.md", "x — y", "z"))
	if err == nil || !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("an ambiguous folded match must be refused: %v", err)
	}
}

// Occurrences that overlap are occurrences all the same: "aba" is in
// "ababa" twice, and taking the first would edit a place the model may
// not have meant, so the edit is refused as ambiguous.
func TestOverlappingExactMatchIsRefused(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("ababa\n"), 0o644)
	e := EditFile{Root: root}
	_, err := e.Run(context.Background(), editIn("a.txt", "aba", "X"))
	if err == nil || !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("an overlapping second match must be refused: %v", err)
	}
	if data, _ := os.ReadFile(p); string(data) != "ababa\n" {
		t.Fatalf("file changed: %q", data)
	}
}

// The span of line i is [starts[i], starts[i+1]); a trailing newline
// leaves an empty last line at len(s), and starts always ends at len(s).
func TestSplitLineSpans(t *testing.T) {
	for _, tc := range []struct {
		in     string
		lines  []string
		starts []int
	}{
		{"", []string{""}, []int{0, 0}},
		{"a", []string{"a"}, []int{0, 1}},
		{"a\n", []string{"a", ""}, []int{0, 2, 2}},
		{"a\nbc\nd", []string{"a", "bc", "d"}, []int{0, 2, 5, 6}},
	} {
		ls := splitLineSpans(tc.in)
		if strings.Join(ls.lines, "|") != strings.Join(tc.lines, "|") || len(ls.lines) != len(tc.lines) {
			t.Errorf("%q lines = %q, want %q", tc.in, ls.lines, tc.lines)
		}
		if len(ls.starts) != len(tc.starts) {
			t.Errorf("%q starts = %v, want %v", tc.in, ls.starts, tc.starts)
			continue
		}
		for i := range ls.starts {
			if ls.starts[i] != tc.starts[i] {
				t.Errorf("%q starts = %v, want %v", tc.in, ls.starts, tc.starts)
				break
			}
		}
	}
}
