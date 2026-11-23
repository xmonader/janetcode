package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repaired(t *testing.T, root, p string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"path": p})
	var out struct{ Path string }
	if err := json.Unmarshal(RepairPaths(context.Background(), nil, root, in), &out); err != nil {
		t.Fatal(err)
	}
	return out.Path
}

func TestRepairPathsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := repaired(t, "/work", "~/x/y.go"); got != filepath.Join(home, "x/y.go") {
		t.Fatalf("~/ : %q", got)
	}
	if got := repaired(t, "/work", "~"); got != home {
		t.Fatalf("~ : %q", got)
	}
}

func TestRepairPathsDroppedSlash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bare := strings.TrimPrefix(home, "/")
	if got := repaired(t, "/work", bare+"/a.go"); got != "/"+bare+"/a.go" {
		t.Fatalf("dropped slash: %q", got)
	}
	if got := repaired(t, "/work", "home/other/a.go"); got != "home/other/a.go" {
		t.Fatalf("another user's path was rewritten: %q", got)
	}
}

func TestRepairPathsDuplicatedWorkspaceName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repaired(t, root, "proj/x.go"); got != "x.go" {
		t.Fatalf("not stripped: %q", got)
	}
	// A real nested directory of the same name wins.
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proj", "x.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repaired(t, root, "proj/x.go"); got != "proj/x.go" {
		t.Fatalf("existing path was rewritten: %q", got)
	}
	// Stripping must not help when the stripped path is missing too.
	if got := repaired(t, root, "proj/nope.go"); got != "proj/nope.go" {
		t.Fatalf("rewritten to a missing path: %q", got)
	}
}

func TestRepairPathsKeepsConfinement(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	in, _ := json.Marshal(map[string]string{"path": "~/secret"})
	fixed := RepairPaths(context.Background(), nil, root, in)
	_, err := ReadFile{Root: root}.Run(context.Background(), fixed)
	if err == nil || !strings.Contains(err.Error(), "outside the working directory") {
		t.Fatalf("repair widened access: %v", err)
	}
}

func TestRepairPathsLeavesOthersAlone(t *testing.T) {
	in := json.RawMessage(`{"command":"ls ~"}`)
	if got := RepairPaths(context.Background(), nil, "/work", in); string(got) != string(in) {
		t.Fatalf("%s", got)
	}
}

func TestMissingFileDidYouMean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(off bool) string {
		_, err := ReadFile{Root: dir, NoRepair: off}.Run(context.Background(), json.RawMessage(`{"path":"confg.go"}`))
		if err == nil {
			t.Fatal("expected error")
		}
		return err.Error()
	}
	if got := read(false); !strings.Contains(got, "did you mean config.go?") {
		t.Fatalf("read: %q", got)
	}
	if got := read(true); got != "confg.go "+NotFound {
		t.Fatalf("off changed read: %q", got)
	}
	in := json.RawMessage(`{"path":"confg.go","old_string":"a","new_string":"b"}`)
	if _, err := (EditFile{Root: dir}).Run(context.Background(), in); err == nil || !strings.Contains(err.Error(), "did you mean config.go?") {
		t.Fatalf("edit: %v", err)
	}
	_, err := EditFile{Root: dir, NoRepair: true}.Run(context.Background(), in)
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("off changed edit: %v", err)
	}
	// No parent directory, no suggestion.
	_, err = ReadFile{Root: dir}.Run(context.Background(), json.RawMessage(`{"path":"nodir/x.go"}`))
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("missing parent: %v", err)
	}
}
