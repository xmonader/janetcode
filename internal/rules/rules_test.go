package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsScopedRulesAndSkipsUnscopedOnes(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".janetcode", "rules", "migrations.md"),
		"---\ndescription: migrations are append-only\npaths: db/migrations/**, *.sql\n---\nNever edit an applied migration.\n")
	// No paths: it would be an always-on instruction, which belongs in
	// JANETCODE.md where its cost is visible.
	write(t, filepath.Join(cwd, ".janetcode", "rules", "everything.md"), "---\ndescription: always\n---\nBe nice.\n")
	got := Load(cwd, "")
	if len(got) != 1 || got[0].Name != "migrations" {
		t.Fatalf("rules = %+v", got)
	}
	if got[0].Description != "migrations are append-only" || !strings.Contains(got[0].Body, "applied migration") {
		t.Fatalf("rule = %+v", got[0])
	}
}

func TestProjectRuleShadowsGlobal(t *testing.T) {
	cwd, cfg := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "rules", "style.md"), "---\npaths: '**/*.go'\n---\nglobal text\n")
	write(t, filepath.Join(cwd, ".janetcode", "rules", "style.md"), "---\npaths: '**/*.go'\n---\nproject text\n")
	got := Load(cwd, cfg)
	if len(got) != 1 || got[0].Body != "project text" {
		t.Fatalf("rules = %+v", got)
	}
}

func TestMatches(t *testing.T) {
	r := Rule{Paths: []string{"db/migrations/**", "*.sql", "internal/tool/"}}
	for _, p := range []string{"db/migrations/001.sql", "./db/migrations/x/y.sql", "schema.sql", "a/b/c.sql"} {
		if !r.Matches(p) {
			t.Errorf("%q should match", p)
		}
	}
	for _, p := range []string{"main.go", "", "docs/db/readme.md"} {
		if r.Matches(p) {
			t.Errorf("%q should not match", p)
		}
	}
}

func TestInjectorSendsEachRuleOnce(t *testing.T) {
	rs := []Rule{
		{Name: "sql", Paths: []string{"*.sql"}, Body: "append only"},
		{Name: "go", Paths: []string{"*.go"}, Body: "run go vet"},
	}
	inj := &Injector{Rules: func() []Rule { return rs }}
	got := inj.For([]string{"db/001.sql"})
	if len(got) != 1 || !strings.Contains(got[0], "append only") {
		t.Fatalf("first = %v", got)
	}
	if !strings.Contains(got[0], "project rule: sql") || !strings.Contains(got[0], "*.sql") {
		t.Fatalf("the text should say which rule and why: %q", got[0])
	}
	if again := inj.For([]string{"db/002.sql"}); len(again) != 0 {
		t.Fatalf("a rule is sent once per session, got %v", again)
	}
	if !inj.Sent("sql") || inj.Sent("go") {
		t.Fatal("Sent does not report what was sent")
	}
	if got := inj.For([]string{"main.go"}); len(got) != 1 || !strings.Contains(got[0], "go vet") {
		t.Fatalf("other rule = %v", got)
	}
	if got := inj.For(nil); got != nil {
		t.Fatalf("no paths, no rules: %v", got)
	}
	var nilInj *Injector
	if got := nilInj.For([]string{"a.sql"}); got != nil {
		t.Fatalf("a nil injector must stay quiet: %v", got)
	}
}
