package tui

import "testing"

func TestFuzzyMatch(t *testing.T) {
	for _, c := range []struct {
		text, q string
		want    bool
	}{
		{"fix the login bug", "login", true},
		{"fix the login bug", "ftlb", true},
		{"fix the login bug", "bgl", false},
		{"anything", "", true},
		{"héllo wörld", "hw", true},
		{"abc", "abcd", false},
	} {
		if got := fuzzyMatch(c.text, c.q); got != c.want {
			t.Errorf("fuzzyMatch(%q,%q)=%v", c.text, c.q, got)
		}
	}
}
