package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
)

func loadLargeTask(t *testing.T, name string) Task {
	t.Helper()
	tasks, err := Load(filepath.Join("..", "..", "evals", "large"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tsk := range FilterLarge(tasks) {
		if tsk.Name == name {
			return tsk
		}
	}
	t.Fatalf("missing %s", name)
	return Task{}
}

func writeTaskFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runTaskCheck(t *testing.T, dir, check string) (ok bool, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := shell.Command(ctx, check)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EVAL_DIR="+dir)
	b, err := cmd.CombinedOutput()
	return err == nil, string(b)
}

func TestLargeRenameSeedUnsolved(t *testing.T) {
	tsk := loadLargeTask(t, "32-large-rename")
	if !strings.Contains(tsk.Files["widget.go"], "func Widget") {
		t.Fatalf("widget.go must define Widget:\n%s", tsk.Files["widget.go"])
	}
	if strings.Contains(tsk.Files["widget.go"], "func Gadget") {
		t.Fatal("seed already renamed Widget to Gadget")
	}
	if !strings.Contains(tsk.Files["widget_test.go"], "Widget()") {
		t.Fatalf("widget_test.go must call Widget:\n%s", tsk.Files["widget_test.go"])
	}
	doc := tsk.Files["pkg/p001/doc.go"]
	if !strings.Contains(doc, "Widget") {
		t.Fatalf("pkg docs must mention Widget:\n%s", doc)
	}
	dir := writeTaskFiles(t, tsk.Files)
	ok, out := runTaskCheck(t, dir, tsk.Check)
	if ok {
		t.Fatalf("unmodified rename seed must fail the shipped check, output:\n%s", out)
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "Widget", "Gadget")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, out = runTaskCheck(t, dir, tsk.Check)
	if !ok {
		t.Fatalf("after Widget→Gadget the shipped check must pass, output:\n%s", out)
	}
}

func TestLargeHeadersSeedCompiles(t *testing.T) {
	tsk := loadLargeTask(t, "34-large-headers")
	h000, okH := tsk.Files["pkg/h000.go"]
	testSrc, okT := tsk.Files["pkg/hdr_test.go"]
	if !okH || !okT {
		t.Fatalf("H000 and hdr_test.go must share pkg/: h000=%v test=%v keys=%d", okH, okT, len(tsk.Files))
	}
	if _, root := tsk.Files["hdr_test.go"]; root {
		t.Fatal("hdr_test.go must not sit at module root (different package from pkg/h000.go)")
	}
	if !strings.Contains(h000, "func H000") || !strings.Contains(testSrc, "H000()") {
		t.Fatalf("test must call H000 in the same package\nh000:\n%s\ntest:\n%s", h000, testSrc)
	}
	dir := writeTaskFiles(t, tsk.Files)
	ok, out := runTaskCheck(t, dir, "go test ./...")
	if !ok {
		t.Fatalf("unmodified headers seed must compile and pass go test ./..., output:\n%s", out)
	}
	ok, out = runTaskCheck(t, dir, tsk.Check)
	if ok {
		t.Fatalf("unmodified headers seed must fail the SPDX check, output:\n%s", out)
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.HasPrefix(s, "// SPDX-License-Identifier: MIT\n") {
			return nil
		}
		return os.WriteFile(path, []byte("// SPDX-License-Identifier: MIT\n"+s), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, out = runTaskCheck(t, dir, tsk.Check)
	if !ok {
		t.Fatalf("after SPDX headers the shipped check must pass, output:\n%s", out)
	}
}

func TestSessionTasksLoad(t *testing.T) {
	tasks, err := Load(filepath.Join("..", "..", "evals", "session"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("%d session tasks", len(tasks))
	}
	for _, task := range tasks {
		if !task.Session() || len(task.Steps()) < 6 || !contains(task.Tags, "session") {
			t.Fatalf("%s: prompts=%d tags=%v", task.Name, len(task.Steps()), task.Tags)
		}
	}
	if rec := tasks[1]; rec.Name != "42-session-records" || len(rec.Files) < 120 || rec.Files[".eval-expected/check.sh"] == "" {
		t.Fatalf("records corpus not expanded: %s %d files", rec.Name, len(rec.Files))
	}
}

// The records session's check script must pass on the right answers and
// fail on the untouched tree, or it measures nothing.
func TestRecordsCheckAcceptsTheCorrectSolution(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	files := corpusRecords()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, ".eval-output.txt")
	run := func() (bool, string) {
		cmd := exec.Command("bash", ".eval-expected/check.sh")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "EVAL_OUTPUT="+out)
		b, err := cmd.CombinedOutput()
		return err == nil, string(b)
	}
	os.WriteFile(out, []byte("nothing yet\n"), 0o644)
	if ok, msg := run(); ok {
		t.Fatalf("check passed on the untouched tree: %s", msg)
	}
	// Do the session's work the way a correct agent would.
	for _, f := range []string{"owners.txt", "top_owner.txt", "sizes.txt"} {
		os.WriteFile(filepath.Join(dir, f), []byte(files[".eval-expected/"+f]), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "inventory.txt"), []byte(files[".eval-expected/inventory.txt"]), 0o644)
	changed := 0
	for name, content := range files {
		if !strings.HasPrefix(name, "data/") || !strings.Contains(content, "owner: carol\n") {
			continue
		}
		changed++
		os.WriteFile(filepath.Join(dir, name), []byte(strings.Replace(content, "id: TOKEN_", "id: ARCHIVED_", 1)), 0o644)
	}
	if ok, msg := run(); ok {
		t.Fatalf("check passed before the inventory was updated: %s", msg)
	}
	os.WriteFile(filepath.Join(dir, "inventory.txt"), []byte(files[".eval-expected/inventory_final.txt"]), 0o644)
	os.WriteFile(out, []byte(fmt.Sprintf("I changed %d files.\n", changed)), 0o644)
	if ok, msg := run(); !ok {
		t.Fatalf("check rejected the correct solution: %s", msg)
	}
	if want := strings.TrimSpace(files[".eval-expected/changed.txt"]); want != fmt.Sprint(changed) {
		t.Fatalf("expected changed = %s, solution changed %d", want, changed)
	}
}
