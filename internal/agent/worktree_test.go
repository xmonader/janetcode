package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The editing subagent's patch is for git apply, so the user's diff taste
// must not reach it: dropped a/ b/ prefixes make git apply -p1 strip a
// real path component, forced colour puts escapes in the header, and an
// external diff tool replaces the patch with its own output.
func TestWorktreePatchIgnoresDiffConfig(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dir"), 0o755)
	os.WriteFile(filepath.Join(root, "dir", "a.txt"), []byte("one\n"), 0o644)
	gitInit(t, root)
	for _, kv := range [][2]string{{"diff.noprefix", "true"}, {"color.ui", "always"}, {"diff.external", "false"}} {
		if out, err := exec.Command("git", "-C", root, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v\n%s", err, out)
		}
	}
	ctx := context.Background()
	wt, err := newWorktree(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.remove(ctx)
	os.WriteFile(filepath.Join(wt.Dir, "dir", "a.txt"), []byte("two\n"), 0o644)
	patch, _, err := wt.diff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// autocrlf off for the test's own apply: Git for Windows' default
	// would write CRLF into root, which is the user's choice there, not
	// something the patch did.
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "apply", "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\npatch:\n%s", err, out, patch)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "dir", "a.txt")); string(b) != "two\n" {
		t.Fatalf("a.txt = %q", b)
	}
}
