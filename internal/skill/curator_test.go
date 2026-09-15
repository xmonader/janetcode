package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCuratorPinArchiveRestore(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".janetcode", "skills", "auto-skill-foo", "SKILL.md"), "---\nsource: auto-skill\ndescription: learned how to foo\n---\nDo foo.\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "hand.md"), "---\ndescription: written by hand\n---\nHand.\n")

	out, err := Curate(cwd, map[string]Stat{"auto-skill-foo": {Used: 3}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "auto-skill-foo") || !strings.Contains(out, "used 3") {
		t.Fatalf("list = %q", out)
	}
	if strings.Contains(out, "hand") {
		t.Fatalf("hand-written skill listed as auto: %q", out)
	}

	if _, err := Curate(cwd, nil, "pin auto-skill-foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := Curate(cwd, nil, "archive auto-skill-foo"); err == nil {
		t.Fatal("pinned skill should not archive")
	}

	st := loadState(cwd)
	st.Pinned = nil
	if err := saveState(cwd, st); err != nil {
		t.Fatal(err)
	}
	if _, err := Curate(cwd, nil, "archive auto-skill-foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".janetcode", "skills", "auto-skill-foo", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("still in skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".janetcode", "archived-skills", "auto-skill-foo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := Curate(cwd, nil, "restore auto-skill-foo"); err != nil {
		t.Fatal(err)
	}
	skills := Load(cwd, "")
	found := false
	for _, s := range skills {
		if s.Name == "auto-skill-foo" && s.Auto() {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored skill missing: %+v", skills)
	}
}

func TestSkillOriginFromName(t *testing.T) {
	s := Skill{Name: "auto-skill-bar"}
	if !s.Auto() {
		t.Fatal("name prefix should count as auto")
	}
}
