package diff

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

func apply(a []string, ops []Op) []string {
	var out []string
	ai := 0
	for _, op := range ops {
		switch op.Kind {
		case ' ':
			if a[ai] != op.Text {
				panic("equal op mismatch")
			}
			out = append(out, op.Text)
			ai++
		case '-':
			if a[ai] != op.Text {
				panic("delete op mismatch")
			}
			ai++
		case '+':
			out = append(out, op.Text)
		}
	}
	if ai != len(a) {
		panic("did not consume a")
	}
	return out
}

func TestLinesRoundTrip(t *testing.T) {
	cases := [][2]string{
		{"", ""},
		{"a\nb\nc", "a\nb\nc"},
		{"a\nb\nc", "a\nx\nc"},
		{"", "a\nb"},
		{"a\nb", ""},
		{"a\nb\nc\nd\ne", "b\nc\nd\ne\nf"},
		{"x\ny\nz", "1\n2\n3"},
		{strings.Repeat("l\n", 3000) + "end", strings.Repeat("l\n", 2999) + "changed\nend"},
	}
	for _, c := range cases {
		a, b := splitLines(c[0]), splitLines(c[1])
		got := apply(a, Lines(a, b))
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Errorf("round trip failed for %q -> %q: got %q", c[0], c[1], got)
		}
	}
}

func TestLinesMinimal(t *testing.T) {
	ops := Lines(splitLines("a\nb\nc\nd"), splitLines("a\nc\nd\ne"))
	var s strings.Builder
	for _, op := range ops {
		s.WriteByte(op.Kind)
		s.WriteString(op.Text)
		s.WriteByte('|')
	}
	if s.String() != " a|-b| c| d|+e|" {
		t.Fatalf("got %s", s.String())
	}
}

func TestUnified(t *testing.T) {
	before := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	after := "one\ntwo\n3\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n"
	got := Unified("f.txt", before, after, 2)
	want := `--- f.txt
+++ f.txt
@@ -1,5 +1,5 @@
 one
 two
-three
+3
 four
 five
@@ -9,2 +9,3 @@
 nine
 ten
+eleven
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if Unified("f", "same\n", "same\n", 3) != "" {
		t.Fatal("identical inputs should produce no diff")
	}
	add, rem := Stats(before, after)
	if add != 2 || rem != 1 {
		t.Fatalf("stats = +%d -%d", add, rem)
	}
	// New file.
	got = Unified("n", "", "a\nb\n", 3)
	if !strings.Contains(got, "@@ -1,0 +1,2 @@\n+a\n+b\n") {
		t.Fatalf("new file diff:\n%s", got)
	}
}

// The edit script has to reconstruct b from a whatever the inputs are,
// including after the banded trace rewrite: the backtrack reads the
// frontier one diagonal either side of the one it is on, and a band that
// is one entry too narrow silently returns a wrong script.
func TestLinesRoundTripRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	line := func() string { return fmt.Sprint(rng.Intn(8)) }
	for i := 0; i < 500; i++ {
		a := make([]string, rng.Intn(40))
		for j := range a {
			a[j] = line()
		}
		b := make([]string, rng.Intn(40))
		for j := range b {
			b[j] = line()
		}
		got := apply(a, Lines(a, b))
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Fatalf("round trip failed\na=%q\nb=%q\ngot=%q", a, b, got)
		}
	}
}

// Diffing a large file that was rewritten from end to end is an ordinary
// write_file call. Keeping the whole frontier for every round of the
// search made that cost hundreds of megabytes.
func TestLargeDissimilarDiffIsBoundedInMemory(t *testing.T) {
	a := make([]string, 4000)
	b := make([]string, 4000)
	for i := range a {
		a[i] = fmt.Sprintf("old line %d", i)
		b[i] = fmt.Sprintf("new line %d", i)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ops := Lines(a, b)
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 200<<20 {
		t.Fatalf("diffing two 4000-line files allocated %d MB", grew>>20)
	}
	if got := apply(a, ops); strings.Join(got, "\n") != strings.Join(b, "\n") {
		t.Fatal("round trip failed on the large diff")
	}
}
