package textfmt

import (
	"testing"
	"time"
)

func TestPlural(t *testing.T) {
	cases := []struct {
		n    int
		word string
		want string
	}{
		{0, "call", "0 calls"},
		{1, "call", "1 call"},
		{2, "call", "2 calls"},
	}
	for _, c := range cases {
		if got := Plural(c.n, c.word); got != c.want {
			t.Errorf("Plural(%d, %q) = %q, want %q", c.n, c.word, got, c.want)
		}
	}
}

func TestRelTo(t *testing.T) {
	cases := []struct {
		cwd, p, want string
	}{
		{"/home/x/proj", "/home/x/proj/main.go", "main.go"},
		{"/home/x/proj", "/other/main.go", "/other/main.go"},
		{"", "/home/x/proj/main.go", "/home/x/proj/main.go"},
		{"/home/x/proj", "relative.go", "relative.go"},
	}
	for _, c := range cases {
		if got := RelTo(c.cwd, c.p); got != c.want {
			t.Errorf("RelTo(%q, %q) = %q, want %q", c.cwd, c.p, got, c.want)
		}
	}
}

func TestTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1200, "1.2k"},
		{12000, "12k"},
		{1_500_000, "1.5M"},
	}
	for _, c := range cases {
		if got := Tokens(c.n); got != c.want {
			t.Errorf("Tokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "<1ms"},
		{420 * time.Millisecond, "420ms"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "1m30s"},
	}
	for _, c := range cases {
		if got := Duration(c.d); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "30m"},
		{5 * time.Hour, "5h"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := Age(c.d); got != c.want {
			t.Errorf("Age(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{2048, "2.0KB"},
		{5 << 20, "5.0MB"},
	}
	for _, c := range cases {
		if got := Bytes(c.n); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hell…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		// Multibyte case: never split a rune.
		{"héllo wörld", 7, "héllo …"},
		{"日本語のテスト", 4, "日本語…"},
	}
	for _, c := range cases {
		if got := Clip(c.s, c.n); got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestClipBytes(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},    // no truncation
		{"hello", 5, "hello"},     // exact fit
		{"hello world", 5, "he…"}, // ASCII truncation
		{"hello", 1, "…"},         // max below ellipsis length
		{"hello", 0, ""},          // max=0
		{"aé", 3, "aé"},           // exact fit with 2-byte char
		{"a中b", 4, "a…"},          // truncate before 3-byte char
		{"a中", 4, "a中"},           // exact fit with 3-byte char
		{"a🎉", 5, "a🎉"},           // exact fit with 4-byte char
		{"a🎉", 3, "…"},            // max=3, can't fit content
	}
	for _, c := range cases {
		if got := ClipBytes(c.s, c.max); got != c.want {
			t.Errorf("ClipBytes(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"one\ntwo\nthree", 0, "one"},
		{"only one line", 0, "only one line"},
		{"a long first line here\nrest", 10, "a long fi…"},
		{"short\nrest", 100, "short"},
	}
	for _, c := range cases {
		if got := FirstLine(c.s, c.max); got != c.want {
			t.Errorf("FirstLine(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

func TestHeadingLine(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"# Title\nbody", 120, "Title"},
		{"### Deep Heading\nrest", 60, "Deep Heading"},
		{"no heading marker\nrest", 60, "no heading marker"},
		{"a very long heading that will need clipping down to size", 20, "a very long heading…"},
		{"  # spaced out  \nrest", 60, "spaced out"},
		{"", 60, ""},
	}
	for _, c := range cases {
		if got := HeadingLine(c.s, c.max); got != c.want {
			t.Errorf("HeadingLine(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

func TestOneLine(t *testing.T) {
	cases := []struct {
		s    string
		want string
	}{
		{"  a\nb\nc  ", "a b c"},
		{"already one line", "already one line"},
		{"\n\ntrim me\n\n", "trim me"},
	}
	for _, c := range cases {
		if got := OneLine(c.s); got != c.want {
			t.Errorf("OneLine(%q) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestSquashAndSplitCSV(t *testing.T) {
	if got := Squash("  a\n\tb   c \r\n"); got != "a b c" {
		t.Errorf("Squash = %q", got)
	}
	if got := SplitCSV(" a, ,b ,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("SplitCSV = %q", got)
	}
	if got := SplitCSV(" , "); got != nil {
		t.Errorf("SplitCSV of nothing = %q, want nil", got)
	}
	if got := HeadingLine("## a heading longer than a cap\nrest", 0); got != "a heading longer than a cap" {
		t.Errorf("HeadingLine with no cap = %q", got)
	}
}
