package tree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestScratchHoldsUncommittedChangesUnderTheProjectPrefix(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	top := t.TempDir()
	root := filepath.Join(top, "sub")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", top}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := Scratch(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "edited\n", "new.txt": "new\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s in the scratch copy: %q %v", name, got, err)
		}
	}
	// What a rerun writes there never reaches the real tree.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("subset\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "edited\n" {
		t.Fatalf("the real tree changed: %q", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the scratch copy outlived cleanup: %v", err)
	}
}
