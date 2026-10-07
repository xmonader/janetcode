//go:build linux

package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

// TestSandboxedBashCannotPlantAGitHook runs a real command under the
// Landlock backend, the one whose kernel rules cannot carve .git/hooks
// out of the project root. The command reaches the hook by a path the
// static deny check cannot read, and rewrites a config key that runs a
// program; neither survives the call, and the result says so. An
// ordinary commit in the same sandbox still works.
func TestSandboxedBashCannotPlantAGitHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	sb := &shell.Sandbox{}
	if err := sb.SetBackend(shell.BackendLandlock); err != nil {
		t.Skipf("no Landlock: %v", err)
	}
	if err := sb.SetProfile("workspace"); err != nil {
		t.Fatal(err)
	}
	if err := sb.Enable(); err != nil {
		t.Skipf("no Landlock: %v", err)
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	b := Bash{Root: root, Sandbox: sb}
	in, _ := json.Marshal(map[string]string{"command": `h=$(printf '%s' .git/hoo; printf ks); printf '#!/bin/sh\necho pwned\n' > "$h/post-commit" && chmod +x "$h/post-commit" && git config core.hooksPath /tmp && echo planted`})
	out, err := b.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "planted") {
		t.Fatalf("the command did not run as the test meant:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatalf("the hook survived the sandboxed command:\n%s", out)
	}
	if cfg, _ := os.ReadFile(filepath.Join(root, ".git", "config")); strings.Contains(strings.ToLower(string(cfg)), "hookspath") {
		t.Fatalf("core.hooksPath survived:\n%s", cfg)
	}
	if !strings.Contains(out, "[sandbox: put back") || !strings.Contains(out, "hooks/post-commit (added)") {
		t.Fatalf("the result does not say what was put back:\n%s", out)
	}

	in, _ = json.Marshal(map[string]string{"command": `echo x > f.txt && git add f.txt && git commit -q -m one && git log --oneline | wc -l`})
	out, err = b.Run(context.Background(), in)
	if err != nil || strings.TrimSpace(strings.Split(out, "\n")[0]) != "1" || strings.Contains(out, "put back") {
		t.Fatalf("an ordinary commit in the sandbox: %v\n%s", err, out)
	}
}
