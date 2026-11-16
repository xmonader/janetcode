package admit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorkspaceModeAllowsEverything(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "a.go", "x")
	if err := New().CheckEdit(root, p, p, "a.go"); err != nil {
		t.Fatalf("workspace mode refused an edit: %v", err)
	}
	var nilPolicy *Policy
	if err := nilPolicy.CheckEdit(root, p, p, "a.go"); err != nil {
		t.Fatalf("nil policy refused an edit: %v", err)
	}
}

func TestExplicitModeAdmitsFilesAndDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	a := write(t, root, "a.go", "x")
	b := write(t, root, "b.go", "x")
	in := write(t, root, "pkg/in.go", "x")
	sibling := write(t, root, "pkg2/other.go", "x") // shares the textual prefix "pkg"
	p := New()
	p.SetMode(Explicit)

	for _, f := range []string{a, b, in, sibling} {
		if err := p.CheckEdit(root, f, f, filepath.Base(f)); err == nil {
			t.Fatalf("%s: edit allowed with nothing admitted", f)
		}
	}
	p.AddEdit(root, a)
	p.AddEdit(root, filepath.Join(root, "pkg"))
	if err := p.CheckEdit(root, a, a, "a.go"); err != nil {
		t.Errorf("admitted file refused: %v", err)
	}
	if err := p.CheckEdit(root, in, in, "pkg/in.go"); err != nil {
		t.Errorf("file under an admitted directory refused: %v", err)
	}
	newFile := filepath.Join(root, "pkg", "deep", "new.go")
	if err := p.CheckEdit(root, newFile, newFile, "pkg/deep/new.go"); err != nil {
		t.Errorf("new file under an admitted directory refused: %v", err)
	}
	if err := p.CheckEdit(root, b, b, "b.go"); err == nil {
		t.Error("a file nobody admitted was allowed")
	}
	if err := p.CheckEdit(root, sibling, sibling, "pkg2/other.go"); err == nil {
		t.Error("pkg2 was allowed because pkg is admitted: the match is textual, not by path")
	}
}

func TestRefusalNamesTheCommandThatAdmits(t *testing.T) {
	root := t.TempDir()
	b := write(t, root, "b.go", "x")
	p := New()
	p.SetMode(Explicit)
	err := p.CheckEdit(root, b, b, "b.go")
	var d *Denied
	if !errors.As(err, &d) {
		t.Fatalf("err = %v, want *Denied", err)
	}
	for _, want := range []string{"b.go", "not admitted", "/admit b.go", "--admit b.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	p.AddAttach(root, b)
	err = p.CheckEdit(root, b, b, "b.go")
	if err == nil || !strings.Contains(err.Error(), "read-only") || !strings.Contains(err.Error(), "/admit b.go") {
		t.Errorf("attached file refusal = %v, want read-only and the admit command", err)
	}
	p.AddEdit(root, b)
	if err := p.CheckEdit(root, b, b, "b.go"); err != nil {
		t.Errorf("admitting an attached file did not allow edits: %v", err)
	}
	if edit, attach := p.Lines(); len(edit) != 1 || len(attach) != 0 {
		t.Errorf("lists after admitting an attached file: edit=%v attach=%v", edit, attach)
	}
}

// A symlink inside an admitted directory that leads to a file nobody
// admitted must not carry an edit there.
func TestSymlinkIsJudgedByWhatItLeadsTo(t *testing.T) {
	root := t.TempDir()
	secret := write(t, root, "secret/key.txt", "k")
	write(t, root, "pkg/ok.go", "x")
	link := filepath.Join(root, "pkg", "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	p := New()
	p.SetMode(Explicit)
	p.AddEdit(root, filepath.Join(root, "pkg"))
	if err := p.CheckEdit(root, link, secret, "pkg/link.txt"); err == nil {
		t.Fatal("an edit through a link to a non-admitted file was allowed")
	}
	p.AddEdit(root, secret)
	if err := p.CheckEdit(root, link, secret, "pkg/link.txt"); err != nil {
		t.Fatalf("link and target both admitted, refused: %v", err)
	}
}

// Entries inside the working directory are kept relative, so the same
// list holds in a worktree or after /cd.
func TestEntriesFollowTheRoot(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	a1 := write(t, one, "src/a.go", "x")
	a2 := write(t, two, "src/a.go", "x")
	b2 := write(t, two, "src/b.go", "x")
	p := New()
	p.SetMode(Explicit)
	p.AddEdit(one, a1)
	if st := p.Snapshot(); len(st.Edit) != 1 || st.Edit[0] != "src/a.go" {
		t.Fatalf("stored entry = %v, want src/a.go", st.Edit)
	}
	if err := p.CheckEdit(two, a2, a2, "src/a.go"); err != nil {
		t.Errorf("same relative file in the other tree refused: %v", err)
	}
	if err := p.CheckEdit(two, b2, b2, "src/b.go"); err == nil {
		t.Error("a different file in the other tree was allowed")
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	a := write(t, root, "a.go", "x")
	d := filepath.Join(root, "dir")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetMode(Explicit)
	p.AddEdit(root, a)
	p.AddAttach(root, d)
	st := p.Snapshot()
	q := New()
	q.Restore(st)
	if q.Mode() != Explicit {
		t.Errorf("mode = %q", q.Mode())
	}
	if got, want := fmt.Sprint(q.Snapshot()), fmt.Sprint(st); got != want {
		t.Errorf("restored %s, want %s", got, want)
	}
	if !(State{}).Zero() || !(State{Mode: Workspace}).Zero() || st.Zero() {
		t.Error("Zero() misjudges a default or non-default state")
	}
}

func TestDirectoryAdmissionShowsABoundedResolvedSet(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		write(t, root, fmt.Sprintf("big/f%02d.go", i), "x")
	}
	write(t, root, "big/.git/config", "x")
	write(t, root, "big/private/key.txt", "x")
	write(t, root, ".aiignore", "private/\n")
	p := New()
	p.AddEdit(root, filepath.Join(root, "big"))
	out := p.Describe(root)
	if !strings.Contains(out, "big/ (30 files:") {
		t.Errorf("summary lacks the resolved count (git and ignored files excluded):\n%s", out)
	}
	if strings.Contains(out, "private") || strings.Contains(out, "config") {
		t.Errorf("summary lists a file the tools may not touch:\n%s", out)
	}
	if !strings.Contains(out, "f00.go") || strings.Contains(out, "f20.go") || !strings.HasSuffix(strings.Split(out, "\n")[2], ", ...)") {
		t.Errorf("summary is not a bounded sample:\n%s", out)
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Summary(root, "empty/"); !strings.Contains(got, "no files yet") {
		t.Errorf("empty directory summary = %q", got)
	}
}

func TestPromptListsAdmittedAndAttached(t *testing.T) {
	root := t.TempDir()
	a := write(t, root, "a.go", "x")
	r := write(t, root, "README.md", "x")
	p := New()
	if p.Prompt(root) != "" {
		t.Error("workspace mode must add nothing to the prompt")
	}
	p.SetMode(Explicit)
	if !strings.Contains(p.Prompt(root), "nothing yet") {
		t.Errorf("empty explicit prompt: %s", p.Prompt(root))
	}
	p.AddEdit(root, a)
	p.AddAttach(root, r)
	got := p.Prompt(root)
	for _, want := range []string{"Admitted for editing", "- a.go", "Attached read-only", "- README.md", "/admit"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"explicit": Explicit, " Workspace ": Workspace, "on": Explicit, "off": Workspace} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMode("strict"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}
