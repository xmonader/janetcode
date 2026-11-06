package eval

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A check that leaves something running (a server it started for a smoke
// test, a background watcher) holds its output pipe open after the shell
// exits. Unsupervised, CombinedOutput waited on that pipe for as long as
// the orphan lived — past the check's own two-minute timeout, and past a
// cancelled run — so one such task stalled the whole eval. The check's
// verdict is its own exit status; the leaked child does not change it.
func TestRunCheckIsNotHeldOpenByALeakedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX background job")
	}
	dir := t.TempDir()
	start := time.Now()
	out, err := runCheck(context.Background(), Task{Check: "sleep 30 & echo ok"}, dir, filepath.Join(dir, "out.txt"), "")
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("check took %s: the leaked child held the pipe past the check", elapsed)
	}
	if err != nil || out != "ok" {
		t.Fatalf("a check that exited 0 must pass: out=%q err=%v", out, err)
	}
}

// A check reads fixtures kept beside its task file through EVAL_TASK_DIR,
// which is absolute even when the task was loaded by a relative path.
func TestRunCheckSeesTheTaskDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	dir := t.TempDir()
	task := Task{Check: `printf %s "$EVAL_TASK_DIR"`, dir: "."}
	out, err := runCheck(context.Background(), task, dir, filepath.Join(dir, "out.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(".")
	if out != want || !filepath.IsAbs(out) {
		t.Fatalf("EVAL_TASK_DIR = %q, want %q", out, want)
	}
}
