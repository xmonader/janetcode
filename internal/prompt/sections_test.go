package prompt

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func names(secs []Section) []string {
	out := make([]string, len(secs))
	for i, s := range secs {
		out[i] = s.Name
	}
	return out
}

func TestSectionOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Override
		want []string
	}{
		{"default", Override{}, []string{SectionBase, SectionHarness, SectionEnvironment, SectionProject}},
		{"append", Override{Append: "also be brief"}, []string{SectionBase, SectionHarness, SectionEnvironment, SectionProject, SectionAppend}},
		// An override's base replaces the harness section with it: the
		// environment and the project's instructions still follow.
		{"base override", Override{Base: "you are a test"}, []string{SectionBase, SectionEnvironment, SectionProject}},
		{"base and append", Override{Base: "you are a test", Append: "x"}, []string{SectionBase, SectionEnvironment, SectionProject, SectionAppend}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(Sections(t.TempDir(), tc.o))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("section order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSectionsAreTheWholePrompt pins the one assembly rule: the prompt is
// the concatenation of the sections, with no separator and nothing else.
func TestSectionsAreTheWholePrompt(t *testing.T) {
	dir := t.TempDir()
	for _, o := range []Override{{}, {Append: "extra"}, {Base: "custom base"}, {Base: "custom base", Append: "extra"}} {
		var sb strings.Builder
		for _, s := range Sections(dir, o) {
			sb.WriteString(s.Text)
		}
		if got := SystemWith(dir, o); got != sb.String() {
			t.Fatalf("SystemWith(%+v) is not the join of its sections:\n%q\nvs\n%q", o, got, sb.String())
		}
	}
}

// TestHeadSectionsComeFirst is the invariant the prompt cache rests on:
// every Head section precedes every volatile one, so the cached prefix is
// a prefix of the prompt rather than a scattering of it.
func TestHeadSectionsComeFirst(t *testing.T) {
	for _, o := range []Override{{}, {Append: "extra"}, {Base: "custom base"}} {
		seenTail := false
		for _, s := range Sections(t.TempDir(), o) {
			if !s.Head {
				seenTail = true
				continue
			}
			if seenTail {
				t.Fatalf("head section %q comes after a volatile one (override %+v)", s.Name, o)
			}
		}
	}
}

var datePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// TestHeadIsCacheStable is the whole reason the sections are ordered:
// nothing in the head may carry the date, the working directory or the
// branch, and the head must be byte-identical between two sessions that
// differ in all three.
func TestHeadIsCacheStable(t *testing.T) {
	a := t.TempDir()
	b := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(filepath.Join(b, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, ".git", "HEAD"), []byte("ref: refs/heads/some-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "AGENTS.md"), []byte("never rewrite history"), 0o644); err != nil {
		t.Fatal(err)
	}

	head := func(cwd string) string {
		var sb strings.Builder
		for _, s := range Sections(cwd, Override{}) {
			if s.Head {
				sb.WriteString(s.Text)
			}
		}
		return sb.String()
	}
	ha, hb := head(a), head(b)
	if ha != hb {
		t.Fatalf("the cache-stable head differs between two working directories")
	}
	if datePattern.MatchString(ha) {
		t.Errorf("the head carries a date; it would fall out of cache every midnight:\n%s", datePattern.FindString(ha))
	}
	for _, volatile := range []string{a, b, "some-branch", "Working directory", "AGENTS.md", "never rewrite history"} {
		if strings.Contains(ha, volatile) {
			t.Errorf("the head carries %q, which differs between sessions", volatile)
		}
	}

	// And the volatile tail is where all of it actually lives.
	tail := strings.TrimPrefix(SystemWith(b, Override{}), hb)
	for _, want := range []string{b, "some-branch", "never rewrite history"} {
		if !strings.Contains(tail, want) {
			t.Errorf("tail is missing %q", want)
		}
	}
	if !datePattern.MatchString(tail) {
		t.Error("tail is missing the date")
	}
}

// TestEnvironmentHeadingOpensTheTail pins the split point providers cut
// on for their cache breakpoint.
func TestEnvironmentHeadingOpensTheTail(t *testing.T) {
	s := System(t.TempDir())
	i := strings.Index(s, EnvironmentHeading)
	if i < 0 {
		t.Fatalf("the prompt has no %q heading", EnvironmentHeading)
	}
	if j := strings.Index(s[i+1:], EnvironmentHeading); j >= 0 {
		t.Fatalf("the prompt has two %q headings; a provider cutting on it would cut in the wrong place", EnvironmentHeading)
	}
	if strings.Contains(s[:i], "Working directory") {
		t.Error("the working directory appears above the cache breakpoint")
	}
}
