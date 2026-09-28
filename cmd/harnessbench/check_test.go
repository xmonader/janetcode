package main

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xmonader/janetcode/internal/eval"
)

// A check that exits 0 while leaving a child holding its output pipe is a
// pass. Supervise cuts the pipe wait short and reports that as
// ErrWaitDelay; read as a failure, it scored every harness 0 on such a
// task here while the native eval runner scored the same run a pass.
func TestRunCheckPassesWhenALeakedChildHoldsThePipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX background job")
	}
	dir := t.TempDir()
	passed, why, out := runCheck(context.Background(), eval.Task{Check: "sleep 30 & echo ok"}, dir, filepath.Join(dir, "out.txt"))
	if !passed {
		t.Fatalf("a check that exited 0 must pass: %s (%q)", why, out)
	}
}
