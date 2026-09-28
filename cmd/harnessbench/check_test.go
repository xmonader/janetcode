package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

// An interrupt that lands while a batch is running kills its tasks
// part-way. Their rows record the kill, not the harness, and the caller
// merges whatever comes back over the last good run's JSON — so the
// interrupted batch must come back empty, like the batches after it.
func TestRunHarnessDropsTheBatchAnInterruptCut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the harness binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "janetcode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	defer cancel()
	cfg := runConfig{Key: "k", JanetBin: bin, RepoRoot: dir, EvalsDir: dir, Timeout: time.Minute, JanetDriver: "eval"}
	tasks := []eval.Task{{Name: "01-a", Prompt: "p", Check: "true"}}
	rs, notes := runHarness(ctx, cfg, "janetcode", tasks, eval.BatchesOfFive(tasks))
	if len(rs) != 0 {
		t.Fatalf("the interrupted batch's rows came back and would overwrite real ones: %+v\n%s", rs, notes)
	}
}
