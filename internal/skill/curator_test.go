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

// Moving a skill file never replaces one of the same name: rename does,
// silently, so restoring an archived auto-skill over one regenerated
// since (or archiving over an older archived copy) lost a file.
func TestCuratorDoesNotMoveOverAFileOfTheSameName(t *testing.T) {
	cwd := t.TempDir()
	skills := filepath.Join(cwd, ".janetcode", "skills")
	write(t, filepath.Join(skills, "auto-skill-bar.md"), "---\nsource: auto-skill\n---\nOld bar.\n")
	if _, err := Curate(cwd, nil, "archive auto-skill-bar"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(skills, "auto-skill-bar.md"), "---\nsource: auto-skill\n---\nNew bar.\n")
	if _, err := Curate(cwd, nil, "restore auto-skill-bar"); err == nil {
		t.Fatal("restore replaced a skill of the same name")
	}
	if data, _ := os.ReadFile(filepath.Join(skills, "auto-skill-bar.md")); !strings.Contains(string(data), "New bar.") {
		t.Fatalf("current skill lost: %q", data)
	}
	if _, err := Curate(cwd, nil, "archive auto-skill-bar"); err == nil {
		t.Fatal("archive replaced an archived skill of the same name")
	}
	if data, _ := os.ReadFile(filepath.Join(cwd, ".janetcode", "archived-skills", "auto-skill-bar.md")); !strings.Contains(string(data), "Old bar.") {
		t.Fatalf("archived skill lost: %q", data)
	}
}

func TestSkillOriginFromName(t *testing.T) {
	s := Skill{Name: "auto-skill-bar"}
	if !s.Auto() {
		t.Fatal("name prefix should count as auto")
	}
}
