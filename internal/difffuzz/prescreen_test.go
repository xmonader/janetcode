package difffuzz

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// difffuzzSeeded is one refactor with a subtle behaviour change that the
// package's public test does not reach.
type difffuzzSeeded struct {
	name, before, after, test string
}

var difffuzzFixtures = []difffuzzSeeded{
	{
		name:   "trim: TrimSpace replaced by Trim of spaces (tabs and newlines kept)",
		before: "package p\n\nimport \"strings\"\n\nfunc Clean(s string) string { return strings.ToLower(strings.TrimSpace(s)) }\n",
		after:  "package p\n\nimport \"strings\"\n\nfunc Clean(s string) string { return strings.ToLower(strings.Trim(s, \" \")) }\n",
		test:   "package p\n\nimport \"testing\"\n\nfunc TestClean(t *testing.T) {\n\tif Clean(\"  Hi \") != \"hi\" {\n\t\tt.Fatal()\n\t}\n}\n",
	},
	{
		name:   "grade: boundary moved by one (>= for >)",
		before: "package p\n\nfunc Grade(score int) string {\n\tif score > 89 {\n\t\treturn \"A\"\n\t}\n\tif score > 79 {\n\t\treturn \"B\"\n\t}\n\treturn \"C\"\n}\n",
		after:  "package p\n\nfunc Grade(score int) string {\n\tswitch {\n\tcase score >= 89:\n\t\treturn \"A\"\n\tcase score > 79:\n\t\treturn \"B\"\n\t}\n\treturn \"C\"\n}\n",
		test:   "package p\n\nimport \"testing\"\n\nfunc TestGrade(t *testing.T) {\n\tif Grade(95) != \"A\" || Grade(85) != \"B\" || Grade(10) != \"C\" {\n\t\tt.Fatal()\n\t}\n}\n",
	},
	{
		name:   "reverse: rune reversal rewritten over bytes",
		before: "package p\n\nfunc Reverse(s string) string {\n\tr := []rune(s)\n\tfor i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {\n\t\tr[i], r[j] = r[j], r[i]\n\t}\n\treturn string(r)\n}\n",
		after:  "package p\n\nfunc Reverse(s string) string {\n\tb := []byte(s)\n\tfor i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {\n\t\tb[i], b[j] = b[j], b[i]\n\t}\n\treturn string(b)\n}\n",
		test:   "package p\n\nimport \"testing\"\n\nfunc TestReverse(t *testing.T) {\n\tif Reverse(\"abc\") != \"cba\" {\n\t\tt.Fatal()\n\t}\n}\n",
	},
	{
		name:   "leap: the 400-year rule dropped in a simplification",
		before: "package p\n\nfunc IsLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }\n",
		after:  "package p\n\nfunc IsLeap(y int) bool {\n\tif y%100 == 0 {\n\t\treturn false\n\t}\n\treturn y%4 == 0\n}\n",
		test:   "package p\n\nimport \"testing\"\n\nfunc TestIsLeap(t *testing.T) {\n\tif !IsLeap(2024) || IsLeap(2023) || IsLeap(1900) {\n\t\tt.Fatal()\n\t}\n}\n",
	},
	{
		name:   "fold: EqualFold replaced by comparing ToLower (differs on Unicode special cases)",
		before: "package p\n\nimport \"strings\"\n\nfunc SameName(a, b string) bool { return strings.EqualFold(a, b) }\n",
		after:  "package p\n\nimport \"strings\"\n\nfunc SameName(a, b string) bool { return strings.ToLower(a) == strings.ToLower(b) }\n",
		test:   "package p\n\nimport \"testing\"\n\nfunc TestSameName(t *testing.T) {\n\tif !SameName(\"Bob\", \"bob\") || SameName(\"a\", \"b\") {\n\t\tt.Fatal()\n\t}\n}\n",
	},
}

// TestDifffuzzPrescreen is the idea's offline pre-screen: five refactors,
// each seeded with a subtle behaviour change that its public test passes
// on both sides of, and how many the fuzzer catches in its budget. Off
// unless JANETCODE_DIFFFUZZ_PRESCREEN is set (it builds and fuzzes five
// modules):
//
//	JANETCODE_DIFFFUZZ_PRESCREEN=10s go test ./internal/difffuzz -run DifffuzzPrescreen -v
func TestDifffuzzPrescreen(t *testing.T) {
	budget, err := time.ParseDuration(os.Getenv("JANETCODE_DIFFFUZZ_PRESCREEN"))
	if err != nil {
		t.Skip("JANETCODE_DIFFFUZZ_PRESCREEN not set to a duration")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	caught := 0
	for _, fx := range difffuzzFixtures {
		root := t.TempDir()
		for name, data := range map[string]string{"go.mod": "module example.com/p\n\ngo 1.22\n", "p.go": fx.after, "p_test.go": fx.test} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		r := Run(context.Background(), Request{Root: root, Budget: budget,
			Files: []File{{Rel: "p.go", Before: []byte(fx.before), After: []byte(fx.after), Existed: true, Exists: true}}})
		if r.Diverged != nil {
			caught++
		}
		t.Logf("%-75s %s (%s)%s", fx.name, r.Summary(), time.Since(start).Round(100*time.Millisecond), r.Output)
	}
	t.Logf("caught %d of %d seeded behaviour changes", caught, len(difffuzzFixtures))
}
