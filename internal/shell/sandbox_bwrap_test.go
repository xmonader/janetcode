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

func TestBwrapConfinesWrites(t *testing.T) {
	sb := bwrapSandbox(t, "workspace")
	root, out := t.TempDir(), outsideDir(t)
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	if o, err := runIn(t, sb, root, "echo in > inside.txt && cat inside.txt"); err != nil || strings.TrimSpace(o) != "in" {
		t.Fatalf("write in root: %q %v", o, err)
	}
	if o, err := runIn(t, sb, root, "echo x > "+filepath.Join(out, "f")); err == nil {
		t.Fatalf("write outside the roots succeeded: %q", o)
	}
	if _, err := os.Stat(filepath.Join(out, "f")); err == nil {
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
