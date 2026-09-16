package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill puts one user-invocable skill in a project's skill
// directory and returns the project.
func writeSkill(t *testing.T, name, desc string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".janetcode", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: " + desc + "\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The completion menu offers user-invocable skills, and the rows are
// cached: reading them means reading every skill file on disk, and the
// menu is rebuilt several times per keystroke. The cache is per working
// directory, so /cd — which is the only thing that moves a session
// between projects — must not serve the old project's skills.
func TestSkillRowsAreCachedPerWorkingDirectory(t *testing.T) {
	first := writeSkill(t, "alpha-skill", "the first project's skill")
	second := writeSkill(t, "beta-skill", "the second project's skill")

	opts := fakeOptions(t)
	opts.Cwd = first
	h := newHarnessFor(t, opts)

	h.keys("/alpha")
	v := plain(h.m.View())
	if !strings.Contains(v, "/alpha-skill") {
		t.Fatalf("the project's skill is not offered:\n%s", v)
	}
	// Reading it again answers the same without going back to disk.
	rows := h.m.skillRows()
	if len(rows) == 0 || &rows[0] != &h.m.skillRows()[0] {
		t.Fatal("the rows were rebuilt for the same working directory")
	}
	// A skill added to the project the session has moved to is found,
	// and the one it came from is not offered any more.
	h.m.opts.Cwd = second
	h.m.transcript.cwd = second
	h.m.input.SetValue("/beta")
	if v = plain(h.m.View()); !strings.Contains(v, "/beta-skill") {
		t.Fatalf("the menu kept the previous project's skills:\n%s", v)
	}
	h.m.input.SetValue("/alpha")
	if v = plain(h.m.View()); strings.Contains(v, "alpha-skill · skill") {
		t.Fatalf("the previous project's skill is still offered:\n%s", v)
	}
}
