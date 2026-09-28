package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineAnchorIdentity(t *testing.T) {
	// Reformatting must not invalidate an anchor; changing the line must.
	same := []string{"func A() int {", "  func A() int {", "func  A()   int {", "\tfunc A() int {\t"}
	first := lineAnchor(same[0])
	for _, l := range same[1:] {
		if lineAnchor(l) != first {
			t.Errorf("whitespace changed the anchor: %q", l)
		}
	}
	if lineAnchor("func B() int {") == first {
		t.Error("a different line should not share the anchor")
	}
	if got := Anchor(12, "func A() int {"); !strings.HasPrefix(got, "12:") || len(got) != 6 {
		t.Fatalf("Anchor rendered %q", got)
	}
}

func TestResolveAnchorFollowsAMovedLine(t *testing.T) {
	lines := []string{"package x", "", "func A() {}", "func B() {}", "func C() {}"}
	ref, err := parseAnchor(Anchor(3, "func A() {}"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := resolveAnchor(ref, lines); err != nil || n != 3 {
		t.Fatalf("in place: %d %v", n, err)
	}
	// Two lines inserted above: the number is stale, the anchor is not.
	moved := append([]string{"// one", "// two"}, lines...)
	if n, err := resolveAnchor(ref, moved); err != nil || n != 5 {
		t.Fatalf("after a shift: %d %v", n, err)
	}
	// The line itself changed: refused, not guessed.
	changed := append([]string{}, lines...)
	changed[2] = "func A(n int) {}"
	if _, err := resolveAnchor(ref, changed); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("a changed line must be refused: %v", err)
	}
	// Two equally near duplicates are not a location.
	dup := []string{"x", "func A() {}", "y", "func A() {}", "z"}
	r2, _ := parseAnchor(Anchor(3, "func A() {}"))
	if _, err := resolveAnchor(r2, dup); err == nil || !strings.Contains(err.Error(), "equally") {
		t.Fatalf("an ambiguous anchor must be refused: %v", err)
	}
	// A bare line number still works, with no claim about content.
	bare, _ := parseAnchor("2")
	if n, err := resolveAnchor(bare, lines); err != nil || n != 2 {
		t.Fatalf("bare number: %d %v", n, err)
	}
	if _, err := parseAnchor("abc"); err == nil {
		t.Fatal("a malformed anchor should say what one looks like")
	}
}

// TestEditByAnchorSurvivesItsOwnBatch is the case line numbers get wrong:
// an edit that changes the number of lines above a later edit.
func TestEditByAnchorSurvivesItsOwnBatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	orig := "package x\n\nfunc A() {}\nfunc B() {}\n"
	os.WriteFile(path, []byte(orig), 0o644)

	out, err := ReadFile{Root: root}.Run(context.Background(), raw(`{"path":"f.go","anchors":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ":") || !strings.Contains(out, "func A() {}") {
		t.Fatalf("read_file did not show anchors:\n%s", out)
	}
	aA, aB := Anchor(3, "func A() {}"), Anchor(4, "func B() {}")

	e := EditFile{Root: root}
	in := raw(`{"path":"f.go","edits":[
{"from_anchor":"` + aA + `","to_anchor":"` + aA + `","new_string":"func Alpha() {}\nfunc Extra() {}"},
{"from_anchor":"` + aB + `","to_anchor":"` + aB + `","new_string":"func Beta() {}"}]}`)
	if _, err := e.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "package x\n\nfunc Alpha() {}\nfunc Extra() {}\nfunc Beta() {}\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	// The advantage over a line number: something inserted above after
	// the read. The number would now point at the wrong line and edit it
	// silently; the anchor follows the line it was aimed at.
	os.WriteFile(path, []byte("// added\n// after\npackage x\n\nfunc A() {}\nfunc B() {}\n"), 0o644)
	if _, err := e.Run(context.Background(), raw(`{"path":"f.go","from_anchor":"`+aA+`","to_anchor":"`+aA+`","new_string":"func Alpha() {}"}`)); err != nil {
		t.Fatalf("an anchor should follow its line: %v", err)
	}
	got, _ = os.ReadFile(path)
	if want := "// added\n// after\npackage x\n\nfunc Alpha() {}\nfunc B() {}\n"; string(got) != want {
		t.Fatalf("anchor followed to the wrong place:\ngot  %q\nwant %q", got, want)
	}
	// The same edit by line number lands on the wrong line, which is the
	// failure anchors exist to prevent.
	os.WriteFile(path, []byte("// added\n// after\npackage x\n\nfunc A() {}\nfunc B() {}\n"), 0o644)
	if _, err := e.Run(context.Background(), raw(`{"path":"f.go","from_line":3,"to_line":3,"new_string":"func Alpha() {}"}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "package x") {
		t.Fatalf("expected the stale line number to clobber the wrong line: %q", got)
	}

	// An anchor read before someone else changed the line is refused.
	os.WriteFile(path, []byte("package x\n\nfunc Changed() {}\n"), 0o644)
	if _, err := e.Run(context.Background(), raw(`{"path":"f.go","from_anchor":"`+aA+`","to_anchor":"`+aA+`","new_string":"x"}`)); err == nil {
		t.Fatal("a stale anchor must be refused")
	}
}

// A blank line has an anchor like any other, and read_file prints it; an
// edit aimed at one has to land. It used to be refused unconditionally —
// "line 2 now reads \"\"" — because an empty line was taken for a line
// past the end of the file. A blank line is not followed when it has
// moved, though: blank lines are everywhere, and the nearest one is not
// evidence of being the one that was read.
func TestResolveAnchorOnABlankLine(t *testing.T) {
	lines := []string{"package x", "", "func A() {}", "", "func B() {}"}
	ref, err := parseAnchor(Anchor(2, ""))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := resolveAnchor(ref, lines); err != nil || n != 2 {
		t.Fatalf("a blank line in place: %d %v", n, err)
	}
	ws, _ := parseAnchor(Anchor(2, "   \t"))
	if n, err := resolveAnchor(ws, lines); err != nil || n != 2 {
		t.Fatalf("a whitespace-only anchor on a blank line: %d %v", n, err)
	}
	// Line 2 is no longer blank; the blank line one below is not taken for
	// it.
	moved := []string{"package x", "import \"y\"", "", "func A() {}"}
	if n, err := resolveAnchor(ref, moved); err == nil {
		t.Fatalf("a moved blank line must be refused, not guessed: got line %d", n)
	}
	// Past the end is still refused.
	far, _ := parseAnchor(Anchor(9, ""))
	if _, err := resolveAnchor(far, lines); err == nil {
		t.Fatal("an anchor past the end must be refused")
	}
}
