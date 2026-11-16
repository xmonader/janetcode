package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchBetweenSnapshotsAndToTheWorkTree(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	r, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.Take(ctx, "before")
	if err != nil {
		t.Fatal(err)
	}
	same, err := r.Patch(ctx, before, nil)
	if err != nil || same != "" {
		t.Fatalf("an unchanged tree must give an empty patch, got %q, %v", same, err)
	}
	write(t, filepath.Join(root, "a.txt"), "a2 edited\n")
	write(t, filepath.Join(root, "new.txt"), "fresh\n") // untracked
	os.Remove(filepath.Join(root, "c.txt"))             // deleted
	mid, err := r.Take(ctx, "mid")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "a3 edited again\n")

	between, err := r.Patch(ctx, before, &mid)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-a1", "+a2 edited", "+fresh", "-c1", "new file mode", "deleted file mode", "3 files changed"} {
		if !strings.Contains(between, want) {
			t.Errorf("snapshot-to-snapshot patch lacks %q:\n%s", want, between)
		}
	}
	if strings.Contains(between, "a3 edited") {
		t.Errorf("a patch between snapshots must not see the later work-tree edit:\n%s", between)
	}
	if !strings.HasSuffix(between, "\n") {
		t.Errorf("a patch must end in a newline to apply")
	}
	now, err := r.Patch(ctx, mid, nil)
	if err != nil || !strings.Contains(now, "-a2 edited") || !strings.Contains(now, "+a3 edited again") {
		t.Fatalf("snapshot-to-tree patch: %q, %v", now, err)
	}

	// The patch applies: replayed on the committed tree it reproduces
	// the mid state.
	clone := t.TempDir()
	tarOut, err := exec.Command("git", "-C", root, "archive", "--format=tar", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	untar := exec.Command("tar", "-x", "-C", clone)
	untar.Stdin = strings.NewReader(string(tarOut))
	if err := untar.Run(); err != nil {
		t.Fatal(err)
	}
	apply := exec.Command("git", "-C", clone, "apply", "--unsafe-paths", "-")
	apply.Dir = clone
	apply.Stdin = strings.NewReader(between)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s", err, out)
	}
	if got := read(filepath.Join(clone, "a.txt")); got != "a2 edited\n" {
		t.Errorf("applied a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(clone, "c.txt")); err == nil {
		t.Errorf("applied patch left the deleted file")
	}
	if _, err := r.Patch(ctx, Snap{}, nil); err == nil {
		t.Errorf("an empty snapshot must be an error")
	}
}
