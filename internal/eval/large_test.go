package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/tool"
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
	cmd := tool.ShellCommand(ctx, check)
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
