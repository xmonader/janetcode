package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func missRun(t *testing.T, e EditFile, path, old, new string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"path": path, "old_string": old, "new_string": new})
	_, err := e.Run(context.Background(), in)
	if err == nil {
		return ""
	}
	return err.Error()
}

func escalateFixture(t *testing.T) (string, string) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	if err := os.WriteFile(p, []byte("package a\n\nfunc compute(alpha int, beta int) int {\n\treturn alpha + beta\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, "a.go"
}

func TestEditEscalateHints(t *testing.T) {
	dir, f := escalateFixture(t)
	e := EditFile{Root: dir, Misses: NewEditMisses(), HaveAst: true}

	got := missRun(t, e, f, "func compute(alpha int, gamma int) int {", "x")
	if !strings.Contains(got, "closest line is 3: func compute(alpha int, beta int) int {") {
		t.Fatalf("no closest line: %q", got)
	}
	if strings.Contains(got, "second failed edit") {
		t.Fatalf("escalated on the first miss: %q", got)
	}
	got = missRun(t, e, f, "qqq www eee", "x")
	if !strings.Contains(got, "no similar line was found") || !strings.Contains(got, "second failed edit on a.go") || !strings.Contains(got, "ast_edit") {
		t.Fatalf("second miss: %q", got)
	}
	got = missRun(t, e, f, "gone text", "return alpha + beta")
	if !strings.Contains(got, "may already be applied") {
		t.Fatalf("already applied: %q", got)
	}
}

func TestEditEscalateResetsOnLanding(t *testing.T) {
	dir, f := escalateFixture(t)
	e := EditFile{Root: dir, Misses: NewEditMisses()}
	missRun(t, e, f, "qqq www eee", "x")
	if got := missRun(t, e, f, "alpha + beta", "alpha - beta"); got != "" {
		t.Fatal(got)
	}
	got := missRun(t, e, f, "qqq www eee", "x")
	if strings.Contains(got, "second failed edit") {
		t.Fatalf("count survived a landed edit: %q", got)
	}
	if got := missRun(t, e, f, "qqq www eee", "x"); strings.Contains(got, "ast_edit") || !strings.Contains(got, "second failed edit") {
		t.Fatalf("without ast_edit: %q", got)
	}
}

func TestEditEscalateOffIsUnchanged(t *testing.T) {
	dir, f := escalateFixture(t)
	e := EditFile{Root: dir}
	for i := 0; i < 3; i++ {
		got := missRun(t, e, f, "qqq www eee", "x")
		if got != "old_string not found in a.go" {
			t.Fatalf("off changed the message: %q", got)
		}
	}
}
