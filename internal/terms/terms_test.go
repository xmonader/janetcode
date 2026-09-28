package terms

import "testing"

// Every form of a word stems to the same term, including a word that ends
// in a silent e: "compile" and "compiling", "file" and "files" must meet,
// or a search for the one never finds the other.
func TestStemJoinsAWordsForms(t *testing.T) {
	for _, forms := range [][]string{
		{"compile", "compiling", "compiled", "compiles"},
		{"file", "files", "filed"},
		{"change", "changes", "changed", "changing"},
		{"fail", "failed", "failing", "fails"},
		{"entry", "entries"},
		{"pass", "passes", "passed", "passing"},
	} {
		want := Stem(forms[0])
		for _, f := range forms[1:] {
			if got := Stem(f); got != want {
				t.Errorf("Stem(%q) = %q, Stem(%q) = %q", forms[0], want, f, got)
			}
		}
	}
}

func TestSplitIndexesALiteralWholeAndByParts(t *testing.T) {
	got := Split("see internal/server/router.go:42 for the Failing tests")
	has := map[string]bool{}
	for _, g := range got {
		has[g] = true
	}
	for _, w := range []string{"internal/server/router.go:42", "router", "42", "fail", "test"} {
		if !has[w] {
			t.Errorf("Split lacks %q: %q", w, got)
		}
	}
	for _, w := range []string{"the", "for"} {
		if has[w] {
			t.Errorf("Split kept stop word %q: %q", w, got)
		}
	}
}
