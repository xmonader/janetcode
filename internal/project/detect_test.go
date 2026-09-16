package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAMarkerDirectoryIsNotAProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, ok := Detect(dir); ok {
		t.Fatalf("a directory called go.mod made it a %s project", p.Kind)
	}
}

func TestPackageJsonIsParsedNotGrepped(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{`{"scripts":{"test":"jest"}}`, "npm test"},
		{`{"scripts":{"build":"tsc"},"devDependencies":{"@x/test":"1"}}`, ""},
		{`{"devDependencies":{"jest":"1"},"config":{"test":"x"}}`, ""},
		{`{"scripts":{"build":"tsc"}}`, ""},
		{`{ not json`, ""},
	} {
		dir := dirWith(t, map[string]string{"package.json": tc.body})
		p, ok := Detect(dir)
		if !ok || p.Kind != "Node" {
			t.Fatalf("%s → %+v ok=%v", tc.body, p, ok)
		}
		if p.Verify != tc.want {
			t.Errorf("%s → verify %q, want %q", tc.body, p.Verify, tc.want)
		}
	}
}

func TestMakefileTargetParsing(t *testing.T) {
	for _, tc := range []struct {
		makefile, want string
	}{
		{"test:\n\tgo test\n", "make test"},
		{"test check:\n\techo\n", "make test"},
		{"lint:\n\techo\ncheck:\n\techo\n", "make check"},
		// A variable assignment is not a target.
		{"test := 1\nall:\n\techo\n", ""},
		{"TESTS = a\ntest = b\nall:\n\techo\n", ""},
		{"check ::= x\nall:\n\techo\n", ""},
		// A special target is not one a user can run on its own, and the
		// real rule is found by its own line.
		{".PHONY: test\nall:\n\techo\n", ""},
		{".PHONY: test\ntest:\n\tgo test\n", "make test"},
		// Double-colon rules and CRLF line endings.
		{"test::\n\tgo test\n", "make test"},
		{"test:\r\n\tgo test\r\n", "make test"},
		// A trailing comment on the target line.
		{"test: ## run the suite\n\tgo test\n", "make test"},
		// A target past what the old eight-target scan looked at.
		{genTargets(20) + "test:\n\tgo test\n", "make test"},
	} {
		p, ok := Detect(dirWith(t, map[string]string{"Makefile": tc.makefile}))
		if !ok || p.Kind != "Make" {
			t.Fatalf("%q → %+v ok=%v", tc.makefile, p, ok)
		}
		if p.Verify != tc.want {
			t.Errorf("%q → verify %q, want %q", tc.makefile, p.Verify, tc.want)
		}
	}
}

func genTargets(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("t")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(":\n\techo\n")
	}
	return b.String()
}

func TestAMakefileWithAVeryLongLineStillFindsItsTargets(t *testing.T) {
	// bufio.Scanner's default 64KiB limit would abandon the file here and
	// report no targets at all.
	long := "BIG = " + strings.Repeat("x", 200*1024) + "\n"
	p, _ := Detect(dirWith(t, map[string]string{"Makefile": long + "test:\n\tgo test\n"}))
	if p.Verify != "make test" {
		t.Fatalf("verify = %q", p.Verify)
	}
}

func TestJavaTestNamesAreCaseSensitive(t *testing.T) {
	for _, p := range []string{"src/TestFoo.java", "src/FooTest.java", "src/FooTests.java"} {
		if !IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	for _, p := range []string{"src/Contestant.java", "src/testing.java", "src/Latest.java"} {
		if IsTestFile(p) {
			t.Errorf("%s is not a test file", p)
		}
	}
}
