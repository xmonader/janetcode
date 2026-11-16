package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/admit"
)

func admitFixture(t *testing.T) (string, *Workspace) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"main.go":      "package main\n\nfunc A() int { return 1 }\n",
		"pkg/in.go":    "package pkg\n",
		"docs/read.md": "# docs\n",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, NewLocal(root)
}

func runTool(tl Tool, in any) (string, error) {
	raw, _ := json.Marshal(in)
	return tl.Run(context.Background(), raw)
}

func TestExplicitModeRefusesEditsToFilesNobodyAdmitted(t *testing.T) {
	root, ws := admitFixture(t)
	ws.Admission().SetMode(admit.Explicit)
	if _, err := ws.Admit("main.go", false); err != nil {
		t.Fatal(err)
	}
	edit := EditFile{Root: root, WS: ws}
	write := WriteFile{Root: root, WS: ws}

	// The admitted file is editable.
	if _, err := runTool(edit, map[string]any{"path": "main.go", "old_string": "return 1", "new_string": "return 2"}); err != nil {
		t.Fatalf("edit of an admitted file: %v", err)
	}
	// Anything else is refused, naming the command that admits it, and is
	// left exactly as it was.
	before, _ := os.ReadFile(filepath.Join(root, "pkg/in.go"))
	for name, call := range map[string]func() error{
		"edit_file": func() error {
			_, err := runTool(edit, map[string]any{"path": "pkg/in.go", "old_string": "package pkg", "new_string": "package changed"})
			return err
		},
		"write_file overwrite": func() error {
			_, err := runTool(write, map[string]any{"path": "pkg/in.go", "content": "x"})
			return err
		},
		"write_file create": func() error {
			_, err := runTool(write, map[string]any{"path": "pkg/new.go", "content": "x"})
			return err
		},
		"ast_edit": func() error {
			_, err := runTool(AstEdit{Root: root, WS: ws}, map[string]any{"path": "pkg/in.go", "add_import": "fmt"})
			return err
		},
		"absolute path": func() error {
			_, err := runTool(write, map[string]any{"path": filepath.Join(root, "pkg/in.go"), "content": "x"})
			return err
		},
		"dot-dot path": func() error {
			_, err := runTool(write, map[string]any{"path": "docs/../pkg/in.go", "content": "x"})
			return err
		},
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "/admit ") || !strings.Contains(err.Error(), "not admitted") {
			t.Errorf("%s: err = %v, want a refusal naming /admit", name, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "pkg/in.go"))
	if string(before) != string(after) {
		t.Errorf("a refused edit changed the file:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg/new.go")); err == nil {
		t.Error("a refused write created a file")
	}
	// Reading is unchanged.
	if out, err := runTool(ReadFile{Root: root, WS: ws}, map[string]any{"path": "pkg/in.go"}); err != nil || !strings.Contains(out, "package pkg") {
		t.Errorf("read in explicit mode: %q %v", out, err)
	}
	// Admitting the refused path is the way out, and the message was right.
	if _, err := ws.Admit("pkg/in.go", false); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(write, map[string]any{"path": "pkg/in.go", "content": "package pkg // ok\n"}); err != nil {
		t.Errorf("write after /admit: %v", err)
	}
}

func TestNotebookEditHonoursAdmission(t *testing.T) {
	dir, _ := copyNotebook(t)
	ws := NewLocal(dir)
	ws.Admission().SetMode(admit.Explicit)
	nb := NotebookEdit{Root: dir, WS: ws}
	in := map[string]any{"path": "report.ipynb", "cell_id": "pi", "source": "x = 1"}
	if _, err := runTool(nb, in); err == nil || !strings.Contains(err.Error(), "/admit report.ipynb") {
		t.Fatalf("notebook edit not admitted: %v", err)
	}
	if _, err := ws.Admit("report.ipynb", false); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(nb, in); err != nil {
		t.Fatalf("notebook edit after admission: %v", err)
	}
}

func TestDefaultWorkspaceModeIsUnchanged(t *testing.T) {
	root, ws := admitFixture(t)
	if _, err := runTool(WriteFile{Root: root, WS: ws}, map[string]any{"path": "anything/new.go", "content": "x"}); err != nil {
		t.Fatalf("workspace mode refused a write: %v", err)
	}
}

func TestAdmitCommandsRespectConfinementAndIgnoreFiles(t *testing.T) {
	root, ws := admitFixture(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, ".aiignore"), []byte("secrets/\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	os.WriteFile(filepath.Join(root, "secrets/k.txt"), []byte("k"), 0o644)
	if _, err := ws.Admit(filepath.Join(outside, "x.txt"), false); err == nil || !strings.Contains(err.Error(), "outside the working directory") {
		t.Errorf("admitting a file outside the workspace: %v", err)
	}
	if _, err := ws.Admit("secrets/k.txt", false); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Errorf("admitting an ignored file: %v", err)
	}
	// An /add-dir root is a root: files under it can be admitted.
	if err := ws.AddDir(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Admit(filepath.Join(outside, "x.txt"), false); err != nil {
		t.Errorf("admitting a file under an /add-dir root: %v", err)
	}
}

// Admission is checked after confinement, never instead of it: a link to
// a place outside the roots stays refused even when the link is admitted.
func TestSymlinkConfinementStillApplies(t *testing.T) {
	root, ws := admitFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	ws.Admission().SetMode(admit.Explicit)
	if _, err := ws.Admit("escape", false); err == nil || !strings.Contains(err.Error(), "outside the working directory") {
		t.Errorf("admitting a link out of the workspace: %v", err)
	}
	// Even a stored entry (a hand-edited session file) does not widen it.
	ws.Admission().AddEdit(root, filepath.Join(root, "escape"))
	_, err := runTool(WriteFile{Root: root, WS: ws}, map[string]any{"path": "escape/x.txt", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "outside the working directory") {
		t.Errorf("write through an admitted link out of the workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Error("a file was written outside the workspace")
	}
}

// A link inside an admitted directory to a file in the workspace nobody
// admitted does not carry an edit there.
func TestLinkInsideAdmittedDirToUnadmittedFile(t *testing.T) {
	root, ws := admitFixture(t)
	if err := os.Symlink(filepath.Join(root, "docs/read.md"), filepath.Join(root, "pkg/link.md")); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	ws.Admission().SetMode(admit.Explicit)
	if _, err := ws.Admit("pkg", false); err != nil {
		t.Fatal(err)
	}
	_, err := runTool(WriteFile{Root: root, WS: ws}, map[string]any{"path": "pkg/link.md", "content": "overwritten"})
	if err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("write through a link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "docs/read.md")); string(b) != "# docs\n" {
		t.Errorf("the link's target changed: %q", b)
	}
}

// A worktree session roots the workspace somewhere else; the list, kept
// relative, applies to the same files there.
func TestAdmissionFollowsAWorkspaceToAWorktree(t *testing.T) {
	root, ws := admitFixture(t)
	ws.Admission().SetMode(admit.Explicit)
	if _, err := ws.Admit("main.go", false); err != nil {
		t.Fatal(err)
	}
	state := ws.Admission().Snapshot()

	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "other.go"), []byte("package main\n"), 0o644)
	ws2 := NewLocal(wt)
	ws2.Admission().Restore(state)
	if _, err := runTool(WriteFile{Root: wt, WS: ws2}, map[string]any{"path": "main.go", "content": "package main // wt\n"}); err != nil {
		t.Errorf("admitted file in the worktree: %v", err)
	}
	if _, err := runTool(WriteFile{Root: wt, WS: ws2}, map[string]any{"path": "other.go", "content": "x"}); err == nil {
		t.Error("unadmitted file in the worktree was written")
	}
	_ = root
}

func TestAdmitDenialIsWhatTheAgentAsksBeforeApproval(t *testing.T) {
	root, ws := admitFixture(t)
	ws.Admission().SetMode(admit.Explicit)
	w := WriteFile{Root: root, WS: ws}
	in, _ := json.Marshal(map[string]any{"path": "main.go", "content": "x"})
	if got := AdmitDenial(w, in); !strings.Contains(got, "/admit main.go") {
		t.Errorf("AdmitDenial = %q", got)
	}
	ws.Admit("main.go", false)
	if got := AdmitDenial(w, in); got != "" {
		t.Errorf("AdmitDenial after admission = %q", got)
	}
	if got := AdmitDenial(ReadFile{Root: root, WS: ws}, in); got != "" {
		t.Errorf("a read tool is never refused by admission: %q", got)
	}
}
