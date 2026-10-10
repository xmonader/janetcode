package tour

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/diff"
)

// classifyFiles classifies the single hunk between two versions of a
// file, the way Build does.
func classifyFiles(t *testing.T, path, before, after string) Class {
	t.Helper()
	hunks := change.MakeHunks(change.DiffLines(diff.SplitLinesKeepEOL(before), diff.SplitLinesKeepEOL(after)))
	if len(hunks) != 1 {
		t.Fatalf("want one hunk, got %d", len(hunks))
	}
	removed, added := sides(hunks[0])
	scope := NewScope()
	scope.Add(path, before, after)
	return Classify(path, removed, added, scope)
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name, path, before, after string
		want                      Class
	}{
		{"go reformat", "a.go", "package a\n\nfunc f() int { return 1+2 }\n", "package a\n\nfunc f() int {\n\treturn 1 + 2\n}\n", Format},
		{"go rename to a fresh name", "a.go", "package a\n\nfunc f(x int) int {\n\treturn x * 2\n}\n", "package a\n\nfunc f(n int) int {\n\treturn n * 2\n}\n", Rename},
		{"go comment only", "a.go", "package a\n\n// f doubles.\nfunc f(x int) int { return x * 2 }\n", "package a\n\n// f returns twice x.\nfunc f(x int) int { return x * 2 }\n", Comment},
		{"go operator change", "a.go", "package a\n\nfunc f(x int) int { return x * 2 }\n", "package a\n\nfunc f(x int) int { return x + 2 }\n", Behaviour},
		// The case that makes a naive "modulo identifiers" check unsafe:
		// both names are live, so swapping one for the other changes
		// what the function returns.
		{"swap to an existing variable", "a.go", "package a\n\nfunc f(a, b int) int {\n\treturn a\n}\n", "package a\n\nfunc f(a, b int) int {\n\treturn b\n}\n", Behaviour},
		{"go true to false", "a.go", "package a\n\nvar on = true\n", "package a\n\nvar on = false\n", Behaviour},
		// The old name survives elsewhere in the file: a partial rename
		// may be a scope change, so it is not taken on trust.
		{"old name still used", "a.go", "package a\n\nvar x = 1\n\nfunc f() int { x := 2; return x }\n", "package a\n\nvar x = 1\n\nfunc f() int { y := 2; return y }\n", Behaviour},
		{"inconsistent mapping", "a.go", "package a\n\nfunc f(p, q int) int { return p + p }\n", "package a\n\nfunc f(r, s int) int { return r + s }\n", Behaviour},
		{"python reindent changes scope", "a.py", "def f(x):\n    if x:\n        y = 1\n    return 2\n", "def f(x):\n    if x:\n        y = 1\n        return 2\n", Behaviour},
		{"python spacing inside a line", "a.py", "def f(x):\n    return x+1\n", "def f(x):\n    return x + 1\n", Format},
		{"python rename", "a.py", "def area(w, h):\n    return w * h\n", "def area(width, h):\n    return width * h\n", Rename},
		{"python keyword swap", "a.py", "def f(x):\n    if x:\n        pass\n", "def f(x):\n    while x:\n        pass\n", Behaviour},
		{"python comment", "a.py", "x = 1  # one\n", "x = 1  # the first\n", Comment},
		// A hash inside a string is not a comment: changing it changes
		// the program's output.
		{"hash in a string", "a.py", "print('#1')\n", "print('#2')\n", Behaviour},
		{"string contents", "a.js", "const s = \"a b\";\n", "const s = \"a  b\";\n", Behaviour},
		{"test file edit", "pkg/a_test.go", "package a\n\nfunc TestF(t *testing.T) { want := 2 }\n", "package a\n\nfunc TestF(t *testing.T) { want := 3 }\n", Test},
		{"test file rename is still mechanical", "tests/test_a.py", "def test_f():\n    got = f()\n", "def test_f():\n    res = f()\n", Rename},
		// Imports name something defined elsewhere, which is not in view:
		// a different imported function passes every other rename check.
		{"import swap", "a.py", "from .tz import UTC\n\nx = UTC\n", "from .tz import localize\n\nx = UTC\n", Behaviour},
		{"prose", "README.md", "Run it.\n", "Run it twice.\n", Comment},
		{"prose rewrap", "README.md", "Run it\nnow.\n", "Run it now.\n", Format},
		{"pure addition", "a.go", "package a\n", "package a\n\nfunc g() {}\n", Behaviour},
		{"blank lines only", "a.go", "package a\n\nfunc g() {}\n", "package a\n\n\n\nfunc g() {}\n", Format},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyFiles(t, c.path, c.before, c.after); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestBuildOrdersByRiskAndCollapsesChurn(t *testing.T) {
	src := "package a\n\nfunc F(x int) int { return x*2 }\n"
	reformatted := "package a\n\nfunc F(x int) int { return x * 2 }\n"
	test := "package a\n\nfunc TestF(t *testing.T) { want := 2 }\n"
	changes := []change.Change{
		{Seq: 1, Path: "a/fmt.go", Existed: true, Before: []byte(src), After: []byte(reformatted), Intent: "gofmt"},
		{Seq: 2, Path: "a/a_test.go", Existed: true, Before: []byte(test), After: []byte(strings.Replace(test, "2", "3", 1)), Intent: "expect three"},
		{Seq: 3, Path: "a/core.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "+", 1)), Intent: "add instead"},
		{Seq: 4, Path: "a/wide.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "-", 1)), Intent: "subtract"},
	}
	reach := func(path, before, after string) int {
		if path == "a/wide.go" {
			return 12
		}
		return 0
	}
	tr := Build(changes, reach)
	var got []string
	for _, it := range tr.Items {
		got = append(got, it.Path+":"+it.Class.String()+":"+it.Intent)
	}
	want := []string{"a/wide.go:behaviour:subtract", "a/core.go:behaviour:add instead", "a/a_test.go:test:expect three"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v, want %v", got, want)
	}
	if len(tr.Mechanical) != 1 || tr.Mechanical[0].Path != "a/fmt.go" {
		t.Errorf("mechanical = %+v", tr.Mechanical)
	}
	if tr.Items[0].Refs != 12 {
		t.Errorf("reach not carried: %+v", tr.Items[0])
	}
	if b := tr.Brief(); !strings.Contains(b, "3 hunks (2 behaviour, 1 test)") || !strings.Contains(b, "2 lines mechanical collapsed") {
		t.Errorf("brief = %q", b)
	}
	out := tr.Format("", false)
	if !strings.Contains(out, "intent: subtract") || !strings.Contains(out, "/tour all shows them") || strings.Contains(out, "a/fmt.go") {
		t.Errorf("format:\n%s", out)
	}
	if all := tr.Format("", true); !strings.Contains(all, "[format] a/fmt.go") {
		t.Errorf("format all:\n%s", all)
	}
}

// A file edited twice is toured once, in its final form, and each hunk
// is attributed to the change that wrote its lines rather than to the
// last change on the file.
func TestBuildNetsAndAttributes(t *testing.T) {
	v0 := "package a\n\nfunc A() int { return 1 }\n\n\n\n\n\n\n\nfunc B() int { return 1 }\n"
	v1 := strings.Replace(v0, "A() int { return 1 }", "A() int { return 2 }", 1)
	v2 := strings.Replace(v1, "B() int { return 1 }", "B() int { return 3 }", 1)
	tr := Build([]change.Change{
		{Seq: 1, Path: "a.go", Existed: true, Before: []byte(v0), After: []byte(v1), Intent: "A returns two"},
		{Seq: 2, Path: "a.go", Existed: true, Before: []byte(v1), After: []byte(v2), Intent: "B returns three"},
	}, nil)
	if len(tr.Items) != 2 {
		t.Fatalf("items = %+v", tr.Items)
	}
	intents := map[string]bool{}
	for _, it := range tr.Items {
		intents[it.Intent] = true
	}
	if !intents["A returns two"] || !intents["B returns three"] {
		t.Errorf("intents = %v", intents)
	}
	if len(tr.Seqs) != 2 {
		t.Errorf("seqs = %v", tr.Seqs)
	}
}

// A rename is judged against every file in the change set: switching a
// call to a function another file of the set still defines is a
// behaviour change, though this file never mentioned either name before.
func TestBuildRenameNeedsTheOldNameGoneEverywhere(t *testing.T) {
	lib0 := "def to_amount(x):\n    return x\n"
	lib1 := lib0 + "\ndef to_exact(x):\n    return x * 1\n"
	use0 := "def price(p):\n    return to_amount(p)\n"
	use1 := "def price(p):\n    return to_exact(p)\n"
	changes := []change.Change{
		{Seq: 1, Path: "lib.py", Existed: true, Before: []byte(lib0), After: []byte(lib1)},
		{Seq: 2, Path: "use.py", Existed: true, Before: []byte(use0), After: []byte(use1)},
	}
	for _, it := range Build(changes, nil).Mechanical {
		t.Errorf("called mechanical: %s %s", it.Path, it.Class)
	}
	// The same call-site change, when lib.py really renamed the function,
	// is a rename.
	lib2 := "def to_exact(x):\n    return x\n"
	changes[0].After = []byte(lib2)
	tr := Build(changes, nil)
	if len(tr.Mechanical) != 2 {
		t.Errorf("true rename across files: mechanical = %+v, items = %+v", tr.Mechanical, tr.Items)
	}
}

func TestBuildEmpty(t *testing.T) {
	if b := Build(nil, nil).Brief(); b != "" {
		t.Errorf("brief of nothing = %q", b)
	}
}

func TestMeter(t *testing.T) {
	src := "package a\n\nfunc F(x int) int { return x*2 }\n"
	changes := []change.Change{
		{Seq: 1, Call: "c1", Path: "/r/pkg/auth/a.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "+", 1))},
		{Seq: 2, Call: "c2", Path: "/r/pkg/auth/b.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "-", 1))},
		{Seq: 3, Call: "c3", Path: "/r/cmd/m.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "/", 1))},
		// Read at the prompt: not debt.
		{Seq: 4, Call: "seen", Path: "/r/cmd/n.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "%", 1))},
		// Mechanical: not debt.
		{Seq: 5, Call: "c5", Path: "/r/cmd/o.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "x*2", "x * 2", 1))},
		// Undone, and the undo itself: not debt.
		{Seq: 6, Call: "c6", Path: "/r/cmd/p.go", Existed: true, Before: []byte(src), After: []byte(strings.Replace(src, "*", "&", 1)), Undone: true},
		{Seq: 7, Tool: "undo", Path: "/r/cmd/p.go", Existed: true, Before: []byte(strings.Replace(src, "*", "&", 1)), After: []byte(src)},
	}
	d := Meter(changes, func(c change.Change) bool { return c.Call == "seen" }, "/r")
	if len(d) != 2 || d[0] != (Debt{"pkg/auth", 4}) || d[1] != (Debt{"cmd", 2}) {
		t.Fatalf("meter = %+v", d)
	}
	if got := DebtLine(d, 1); got != "comprehension debt: 6 lines approved unread · 4 in pkg/auth, 2 more in 1 package" {
		t.Errorf("line = %q", got)
	}
	if got := DebtLine(nil, 3); got != "" {
		t.Errorf("empty line = %q", got)
	}
}
