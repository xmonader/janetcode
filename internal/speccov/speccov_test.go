package speccov

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const brief = "# Task: `calc`\n" +
	"\n" +
	"Build the package `calc/`. It is a small thing.\n" +
	"The parser must reject `1 +` with `unexpected end`.\n" +
	"\n" +
	"## Errors\n" +
	"\n" +
	"| situation | message |\n" +
	"|---|---|\n" +
	"| division by zero | `division by zero at 7` |\n" +
	"| unknown name | `unknown name 'foo'` |\n" +
	"\n" +
	"* `round_half` rounds 2.5 to 3,\n" +
	"  and -2.5 to -3.\n" +
	"* x\n" +
	"\n" +
	"```python\n" +
	"from calc import evaluate\n" +
	"evaluate(\"1+2\") -> 3\n" +
	"evaluate(\"2*3\") -> 2024-01-01 10:15\n" +
	"\"1/0\" -> division by zero at 7\n" +
	"```\n" +
	"\n" +
	"```\n" +
	"$ python -m calc \"1+1\"\n" +
	"2\n" +
	"$ python -m calc \"1/0\"\n" +
	"error: division by zero at 7      (stderr, exit 1)\n" +
	"$ python -m calc <EXPR>\n" +
	"...\n" +
	"```\n"

func itemTexts(sp Spec) []string {
	var out []string
	for _, it := range sp.Items {
		out = append(out, it.ID+" "+it.Kind.String()+" "+it.Section+": "+it.Text)
	}
	return out
}

// Bullets, table rows and sentences with a modal word are rules; a plain
// sentence, a one-word bullet and a table header are not; arrow lines and
// shell sessions in fences are examples.
func TestExtract(t *testing.T) {
	sp := Extract(brief)
	got := strings.Join(itemTexts(sp), "\n")
	for _, want := range []string{
		"R1 rule Task: `calc`: The parser must reject `1 +` with `unexpected end`.",
		"rule Errors: division by zero | `division by zero at 7`",
		"rule Errors: `round_half` rounds 2.5 to 3, and -2.5 to -3.",
		`example Errors: evaluate("1+2") -> 3`,
		`example Errors: $ python -m calc "1+1" ⏎ 2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, not := range []string{"It is a small thing", "situation | message", ": x\n"} {
		if strings.Contains(got+"\n", not) {
			t.Errorf("kept %q in\n%s", not, got)
		}
	}
	if len(sp.Fences) != 2 || sp.Fences[0].Lang != "python" {
		t.Fatalf("fences = %+v", sp.Fences)
	}
}

func TestAnchors(t *testing.T) {
	got := anchors(Item{Text: "It's `round_half` and \"quoted text\" and 'x y' with 60 and 7 and pkg.call(f)"})
	want := []string{"round_half", "quoted text", "x y", "60", "pkg.call"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("anchors = %q, want %q", got, want)
	}
	// An apostrophe is not a quote, and a single digit is too common.
	for _, a := range got {
		if a == "s `round_half` and \"quoted text\" and " || a == "7" {
			t.Fatalf("bad anchor %q", a)
		}
	}
}

func TestContainsAnchor(t *testing.T) {
	for _, c := range []struct {
		text, a string
		want    bool
	}{
		{`parse("60 * * * *")`, "60", true},
		{`x = 160`, "60", false},
		{`target = 1`, "get", false},
		{`c.get(1)`, "get", true},
		{`parse("*/15 9 * * *")`, "*/15", true},
		{`"x5-3y"`, "5-3", false},
		{``, "a", false},
	} {
		if got := ContainsAnchor(c.text, c.a); got != c.want {
			t.Errorf("ContainsAnchor(%q, %q) = %v", c.text, c.a, got)
		}
	}
}

func TestCover(t *testing.T) {
	sp := Spec{Items: []Item{
		{ID: "R1", Text: "search returns the leftmost match: `a|ab`", Anchors: []string{"search", "a|ab"}},
		{ID: "R2", Text: "def search(pattern)", Anchors: []string{"search"}},
		{ID: "R3", Text: "errors say `bad escape`", Anchors: []string{"bad escape"}},
		{ID: "R4", Text: "anything goes", Anchors: nil},
	}}
	tests := map[string]string{"test_x.py": `assert search("a", "a")` + "\n" + `assert err == "bad escape \\q"`}
	m := Cover(sp, tests)
	want := []Status{Uncovered, Covered, Covered, Unanchored}
	for i, r := range m.Rows {
		if r.Status != want[i] {
			t.Errorf("%s = %v, want %v (specific %q hits %q)", r.ID, r.Status, want[i], r.Specific, r.Hits)
		}
	}
	// R1 has a literal besides the name, so calling search is not enough.
	if got := m.Rows[0].Specific; len(got) != 1 || got[0] != "a|ab" {
		t.Fatalf("R1 specific = %q", got)
	}
	if n := m.Count(Uncovered); n != 1 {
		t.Fatalf("uncovered = %d", n)
	}
	if got := Touches(m, `search("a|ab", s)`); len(got) != 2 {
		t.Fatalf("touches = %+v", got)
	}
}

// An anchor most items share tells none of them apart.
func TestSpecificDropsCommonAnchors(t *testing.T) {
	var items []Item
	for i := 0; i < 8; i++ {
		items = append(items, Item{Anchors: []string{"cronx.parse", "rare" + string(rune('a'+i))}})
	}
	spec := Specific(items)
	for i, s := range spec {
		if len(s) != 1 || !strings.HasPrefix(s[0], "rare") {
			t.Fatalf("item %d specific = %q", i, s)
		}
	}
}

func TestNote(t *testing.T) {
	sp := Extract(brief)
	if Note(Cover(sp, nil)) != "" {
		t.Fatal("a note with no public tests to compare against")
	}
	m := Cover(sp, map[string]string{"test_calc.py": `assert evaluate("1+2") == 3` + "\n" + `"unexpected end"`})
	note := Note(m)
	if !strings.HasPrefix(note, "[harness] Spec coverage:") || !strings.Contains(note, "test_calc.py") {
		t.Fatalf("note = %q", note)
	}
	if !strings.Contains(note, "division by zero at 7") || strings.Contains(note, "unexpected end") {
		t.Fatalf("note lists the wrong items: %q", note)
	}
	// Everything covered: nothing to say.
	all := map[string]string{"t_test.go": ""}
	for _, r := range m.Rows {
		for _, a := range r.Specific {
			all["t_test.go"] += " " + a + " "
		}
	}
	if note := Note(Cover(sp, all)); note != "" {
		t.Fatalf("note with every item covered = %q", note)
	}
}

func TestNoteCapsItems(t *testing.T) {
	var m Matrix
	m.Tests = []string{"a_test.go"}
	for i := 0; i < noteMaxItems+3; i++ {
		m.Rows = append(m.Rows, Row{Item: Item{ID: "R", Text: strings.Repeat("y", 400)}, Status: Uncovered})
	}
	note := Note(m)
	if !strings.Contains(note, "- and 3 more") || strings.Count(note, "\n- R") != noteMaxItems {
		t.Fatalf("note = %q", note)
	}
}

func TestTaskTextAndPublicTests(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("BRIEF.md", "the brief")
	write("test_calc.py", "def test_a(): pass")
	write("calc/calc_test.go", "package calc")
	write("web/app.test.ts", "test('x')")
	write("node_modules/dep/test_dep.py", "no")
	write(".hidden/test_h.py", "no")
	write("calc/calc.py", "no")
	text, files := TaskText(root, "Read BRIEF.md and ../secret.md and MISSING.md.")
	if !strings.HasSuffix(text, "\n\nthe brief") || len(files) != 1 || files[0] != "BRIEF.md" {
		t.Fatalf("text %q files %q", text, files)
	}
	got := PublicTests(root)
	if len(got) != 3 || got["test_calc.py"] == "" || got["calc/calc_test.go"] == "" || got["web/app.test.ts"] == "" {
		t.Fatalf("tests = %v", got)
	}
}

func TestHarvest(t *testing.T) {
	sp := Extract(brief)
	ps := Harvest(sp, Imports(sp, nil))
	var labels []string
	for _, p := range ps {
		labels = append(labels, p.Shape+" "+p.Label)
	}
	want := []string{`python evaluate("1+2") -> 3`, `cli $ python -m calc "1+1"`, `cli $ python -m calc "1/0"`}
	if strings.Join(labels, "\n") != strings.Join(want, "\n") {
		t.Fatalf("harvested\n%s\nwant\n%s", strings.Join(labels, "\n"), strings.Join(want, "\n"))
	}
	if ps[0].ItemID == "" || !strings.Contains(ps[0].Cmd, "from calc import evaluate") {
		t.Fatalf("python probe = %+v", ps[0])
	}
	// Without anything to import, no Python example is compiled.
	for _, p := range Harvest(sp, nil) {
		if p.Shape == "python" {
			t.Fatalf("compiled %q with no imports", p.Label)
		}
	}
}

func TestPyLiteral(t *testing.T) {
	for s, want := range map[string]bool{
		`3`: true, `-2.5`: true, `'a'`: true, `[('a', '1'), ('c', '3')]`: true, `{"k": None}`: true,
		`True`: true, `2024-01-01 10:15`: false, `bool`: false, `(1,`: false, `x + 1`: false, ``: false,
		`'it''s'`: true, `"unterminated`: false,
	} {
		if got := pyLiteral(s); got != want {
			t.Errorf("pyLiteral(%q) = %v", s, got)
		}
	}
}

func TestImports(t *testing.T) {
	sp := Extract("```python\nfrom pkg import f\nimport pytest\n```\n")
	got := Imports(sp, map[string]string{
		"b_test.py":    "import os, sys\nfrom pkg import f\n    import inner\n",
		"test_a.py":    "from datetime import datetime\n",
		"main_test.go": "import \"fmt\"\n",
	})
	want := "from pkg import f|import os, sys|from datetime import datetime"
	if strings.Join(got, "|") != want {
		t.Fatalf("imports = %q", got)
	}
}

// The compiled probes run for real: a matching answer passes, a wrong one
// fails with both answers shown, and one the imports do not explain is not
// runnable rather than failed.
func TestProbesRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "calc"), 0o755)
	os.WriteFile(filepath.Join(root, "calc", "__init__.py"), []byte("def evaluate(s):\n    return eval(s)\n"), 0o644)
	os.WriteFile(filepath.Join(root, "calc", "__main__.py"), []byte(
		"import sys\ntry:\n    print(eval(sys.argv[1]))\nexcept ZeroDivisionError:\n    print('error: division by zero at 7', file=sys.stderr)\n    sys.exit(1)\n"), 0o644)
	run := func(cmd string) (string, int) {
		c := exec.Command("bash", "-c", cmd)
		c.Dir = root
		c.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
		out, err := c.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	imports := []string{"from calc import evaluate"}
	for _, c := range []struct {
		lhs, rhs string
		code     int
		out      string
	}{
		{`evaluate("1+2")`, `3`, 0, ""},
		{`evaluate("1+2")`, `4`, 1, "want 4"},
		{`nosuch("1")`, `1`, 3, NotRunnable},
		{`evaluate("1/0")`, `1`, 1, "ZeroDivisionError"},
	} {
		p, ok := pythonProbe(c.lhs, c.rhs, imports)
		if !ok {
			t.Fatalf("%s did not compile", c.lhs)
		}
		out, code := run(p.Cmd)
		if code != c.code || !strings.Contains(out, c.out) {
			t.Errorf("%s -> %s: exit %d, output %q", c.lhs, c.rhs, code, out)
		}
	}
	sp := Extract(brief)
	for _, p := range Harvest(sp, nil) {
		if out, code := run(p.Cmd); code != 0 {
			t.Errorf("%s: exit %d\n%s", p.Label, code, out)
		}
	}
	wrong, _ := cliProbe(`python -m calc "1+1"`, []string{"3"})
	if out, code := run(wrong.Cmd); code != 1 || !strings.Contains(out, "want (exit 0):\n3") {
		t.Errorf("a wrong CLI answer: exit %d, output %q", code, out)
	}
	missing, _ := cliProbe(`no-such-command-janetcode x`, []string{"x"})
	if out, code := run(missing.Cmd); code != 3 || !strings.Contains(out, NotRunnable) {
		t.Errorf("a missing command: exit %d, output %q", code, out)
	}
}

// Shell examples with a placeholder or an elision are not compiled.
func TestCliProbeSkipsPlaceholders(t *testing.T) {
	for _, c := range []struct {
		cmd string
		out []string
	}{
		{"tool <file>", []string{"ok"}},
		{"tool [-n N]", []string{"ok"}},
		{"tool x", []string{"line 1", "..."}},
		{"tool x", nil},
	} {
		if _, ok := cliProbe(c.cmd, c.out); ok {
			t.Errorf("compiled %q %q", c.cmd, c.out)
		}
	}
	p, ok := cliProbe("tool x", []string{"error: bad (stderr, exit 2)", ""})
	if !ok || !strings.Contains(p.Cmd, "2>&1 >/dev/null") || !strings.Contains(p.Cmd, "-ne 2") || strings.Contains(p.Cmd, "(stderr") {
		t.Fatalf("annotated probe = %q", p.Cmd)
	}
}
