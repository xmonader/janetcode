package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bwrapSandbox(t *testing.T, profile string) *Sandbox {
	t.Helper()
	sb := &Sandbox{}
	if !sb.bwrapUsable() {
		t.Skip("bwrap cannot run on this machine")
	}
	if err := sb.SetBackend(BackendBwrap); err != nil {
		t.Fatal(err)
	}
	if err := sb.SetProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := sb.Enable(); err != nil {
		t.Fatal(err)
	}
	return sb
}

func runIn(t *testing.T, sb *Sandbox, root, command string) (string, error) {
	t.Helper()
	cmd, err := sb.Command(context.Background(), root, command)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// outsideDir is a directory in no write root: not the project, and not
// the temp dir every test directory lives in.
func outsideDir(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	d, err := os.MkdirTemp(wd, "outside-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// A sandboxed command must not be able to plant a hook or a config key
// (core.fsmonitor, core.sshCommand) that git runs on the host later.
func TestBwrapGitConfigAndHooksReadOnly(t *testing.T) {
	sb := bwrapSandbox(t, "workspace")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644)
	if o, err := runIn(t, sb, root, "echo '[core]' >> .git/config"); err == nil {
		t.Fatalf(".git/config writable: %q", o)
	}
	if o, err := runIn(t, sb, root, "echo x > .git/hooks/pre-commit"); err == nil {
		t.Fatalf(".git/hooks writable: %q", o)
	}
	if o, err := runIn(t, sb, root, "echo x > .git/HEAD"); err != nil {
		t.Fatalf("the rest of .git must stay writable: %q %v", o, err)
	}
}

// writeProbe is a command whose target path is built at run time, so only
// the kernel can refuse it: no analysis of the command text sees a path.
func writeProbe(dir string) string {
	return `d=` + dir + `; f=$d/probe_$$.txt; echo x > "$f"`
}

// controlWriteOutside proves a probe is meaningful: with no sandbox at all,
// the same write into dir succeeds. A refusal under a sandbox then means
// the sandbox refused it, not that the directory was unwritable anyway.
func controlWriteOutside(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("sh", "-c", writeProbe(dir)).CombinedOutput(); err != nil {
		t.Skipf("control: the unsandboxed probe cannot write to %s: %v\n%s", dir, err, out)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "probe_*.txt"))
	if len(matches) == 0 {
		t.Fatalf("control: the unsandboxed probe left no file in %s", dir)
	}
	for _, m := range matches {
		os.Remove(m)
	}
}

func TestBwrapConfinesWrites(t *testing.T) {
	sb := bwrapSandbox(t, "workspace")
	root, out := t.TempDir(), outsideDir(t)
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	if o, err := runIn(t, sb, root, "echo in > inside.txt && cat inside.txt"); err != nil || strings.TrimSpace(o) != "in" {
		t.Fatalf("write in root: %q %v", o, err)
	}
	controlWriteOutside(t, out)
	if o, err := runIn(t, sb, root, writeProbe(out)); err == nil {
		t.Fatalf("write outside the roots succeeded: %q", o)
	}
	if m, _ := filepath.Glob(filepath.Join(out, "probe_*.txt")); len(m) > 0 {
		t.Fatal("file appeared outside the roots")
	}
	// The command analysis would refuse a named write into .janetcode, so
	// the mount is tested with a write the analysis cannot see.
	if o, err := runIn(t, sb, root, `p=.janet; p=${p}code; echo x > "$p/state"`); err == nil {
		t.Fatalf("write into .janetcode succeeded: %q", o)
	}
}

func TestBwrapStrictHasNoNetworkAndReadOnlyGit(t *testing.T) {
	sb := bwrapSandbox(t, "strict")
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skip("git unavailable")
	}
	if o, err := runIn(t, sb, root, `d=.g; d=${d}it; touch "$d/x"`); err == nil {
		t.Fatalf(".git writable under strict: %q", o)
	}
	o, _ := runIn(t, sb, root, "cat /proc/net/dev | tail -n +3 | cut -d: -f1 | tr -d ' ' | sort | tr '\n' ,")
	if strings.TrimSpace(o) != "lo," {
		t.Fatalf("interfaces under strict = %q, want only lo", o)
	}
}

// Killing the sandboxed command kills what it left running.
func TestBwrapKillTakesChildren(t *testing.T) {
	sb := bwrapSandbox(t, "workspace")
	root := t.TempDir()
	marker := filepath.Join(root, "late")
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := sb.Command(ctx, root, "(sleep 1; touch late) & sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	cmd.Wait()
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a background child outlived the killed command")
	}
}
