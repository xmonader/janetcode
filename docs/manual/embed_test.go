package manual

import (
	"strings"
	"testing"
)

func TestEveryPageIsEmbedded(t *testing.T) {
	want := []string{"README", "ci", "cli", "configuration", "editor", "evals", "interface", "sessions"}
	got := map[string]bool{}
	for _, p := range Pages() {
		got[p] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s.md is not embedded", w)
		}
	}
}

func TestHeadingsInFencedCodeAreNotSections(t *testing.T) {
	// interface.md has a fenced transcript whose lines start with "#".
	for _, s := range Sections() {
		if strings.HasPrefix(s.Title, "worktree:") || strings.HasPrefix(s.Title, "…") {
			t.Fatalf("a line inside a code fence became the section %q", s.Title)
		}
	}
}

func TestFindLandsOnTheRightSection(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"verification gates", "Verification gates"},
		{"permission rules", "Permission rules"},
		{"background jobs", "Background jobs"},
	} {
		hits := Find(tc.query, 3)
		if len(hits) == 0 {
			t.Errorf("%q found nothing", tc.query)
			continue
		}
		found := false
		for _, h := range hits {
			if h.Title == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q found %q, not %q", tc.query, hits[0].Title, tc.want)
		}
	}
}

func TestExcerptIsBoundedAndLabelled(t *testing.T) {
	hits := Find("verification gates", 1)
	if len(hits) == 0 {
		t.Fatal("no hit to excerpt")
	}
	ex := hits[0].Excerpt(300)
	if len(ex) > 420 {
		t.Fatalf("excerpt is %d bytes", len(ex))
	}
	if !strings.HasPrefix(ex, "docs/manual/") {
		t.Fatalf("excerpt does not say where it came from:\n%s", ex)
	}
}

func TestSectionsAreParsedOnce(t *testing.T) {
	a, b := Sections(), Sections()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("%d then %d sections", len(a), len(b))
	}
	if &a[0] != &b[0] {
		t.Fatal("the manual was parsed twice")
	}
}
