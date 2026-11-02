package rules

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseCursor(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     CursorRule
	}{
		{"always", "---\ndescription: house style\nalwaysApply: true\n---\nUse tabs.\n", CursorRule{Always: true, Description: "house style", Body: "Use tabs."}},
		{"globs string", "---\nglobs: src/**/*.ts, **/*.tsx\nalwaysApply: false\n---\nbody", CursorRule{Globs: []string{"src/**/*.ts", "**/*.tsx"}, Body: "body"}},
		{"globs list", "---\nglobs:\n  - \"*.go\"\n  - cmd/**\n---\nbody", CursorRule{Globs: []string{"*.go", "cmd/**"}, Body: "body"}},
		{"globs bracket", "---\nglobs: [\"a/*.py\", 'b/*.py']\n---\nbody", CursorRule{Globs: []string{"a/*.py", "b/*.py"}, Body: "body"}},
		{"description only", "---\ndescription: when X\n---\nbody", CursorRule{Description: "when X", Body: "body"}},
		{"no frontmatter", "just text\n", CursorRule{Body: "just text"}},
		{"bom and crlf", "\xEF\xBB\xBF---\r\nalwaysApply: TRUE\r\n---\r\nb\r\n", CursorRule{Always: true, Body: "b"}},
		{"unclosed", "---\nalwaysApply: true\nbody", CursorRule{Body: "---\nalwaysApply: true\nbody"}},
	} {
		got := ParseCursor(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestLoadReadsCursorGlobRulesOnly(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".cursor/rules/ts.mdc"), "---\nglobs: **/*.ts\n---\nNo any.")
	write(t, filepath.Join(cwd, ".cursor/rules/sub/go.md"), "---\nglobs: '*.go'\n---\nWrap errors.")
	write(t, filepath.Join(cwd, ".cursor/rules/always.mdc"), "---\nalwaysApply: true\n---\nTabs.")
	write(t, filepath.Join(cwd, ".cursor/rules/manual.mdc"), "---\ndescription: fetch me\n---\nrarely")
	write(t, filepath.Join(cwd, ".janetcode/rules/own.md"), "---\npaths: '*.sql'\n---\nown")
	var names []string
	for _, r := range Load(cwd, "") {
		names = append(names, r.Name)
	}
	want := []string{"cursor:sub/go", "cursor:ts", "own"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("rules %v want %v", names, want)
	}
}

// A cursor rule is sent once per session after a matching path is touched.
func TestCursorRuleInjectedOncePerSession(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".cursor/rules/ts.mdc"), "---\nglobs: src/**/*.ts\n---\nNo any.")
	inj := &Injector{Rules: func() []Rule { return Load(cwd, "") }}
	if got := inj.For([]string{"README.md"}); len(got) != 0 {
		t.Fatalf("unmatched path brought a rule: %v", got)
	}
	got := inj.For([]string{"src/a/b.ts"})
	if len(got) != 1 || !strings.Contains(got[0], "No any.") || !strings.Contains(got[0], "cursor:ts") {
		t.Fatalf("first match: %v", got)
	}
	if got := inj.For([]string{"src/c.ts"}); len(got) != 0 {
		t.Fatalf("sent twice: %v", got)
	}
}

func TestCursorRulesAreBounded(t *testing.T) {
	cwd := t.TempDir()
	for i := 0; i < maxCursorFiles+10; i++ {
		write(t, filepath.Join(cwd, ".cursor/rules", strings.Repeat("a", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))+".mdc"), "---\nalwaysApply: true\n---\nx")
	}
	if n := len(CursorRules(cwd)); n != maxCursorFiles {
		t.Fatalf("read %d rules, want cap %d", n, maxCursorFiles)
	}
}
