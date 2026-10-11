package tui

import (
	"testing"
	"unicode/utf8"
)

// Two names that differ in an accented letter share its first byte; the
// shared prefix used to end inside that character.
func TestCommonPrefixKeepsWholeCharacters(t *testing.T) {
	for _, c := range [][3]string{
		{"/open café.txt", "/open cafè.txt", "/open caf"},
		{"/open 日本.md", "/open 日記.md", "/open 日"},
		{"/open abc", "/open abd", "/open ab"},
		{"same", "same", "same"},
	} {
		got := commonPrefix(c[0], c[1])
		if got != c[2] || !utf8.ValidString(got) {
			t.Errorf("commonPrefix(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
