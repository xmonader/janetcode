package verify

import (
	"context"
	"testing"
	"time"
)

func TestRunRecordsDurationNotRunAndCancellation(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Cwd: root, Config: Config{Commands: []string{"sleep 0.05", "exit 4", "echo b", "echo c"}}}
	res := r.Run(context.Background())
	if len(res.Runs) != 2 || res.Runs[0].Duration < 40*time.Millisecond || res.Runs[0].Cancelled || res.Runs[1].Cancelled {
		t.Fatalf("runs = %+v", res.Runs)
	}
	if len(res.NotRun) != 2 || res.NotRun[0] != "echo b" || res.NotRun[1] != "echo c" || res.Cancelled {
		t.Fatalf("a failure leaves the rest not run: %+v", res)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	r = &Runner{Cwd: root, Config: Config{Commands: []string{"sleep 30", "echo after"}}}
	res = r.Run(ctx)
	if !res.Cancelled || len(res.Runs) != 1 || !res.Runs[0].Cancelled || len(res.NotRun) != 1 || res.NotRun[0] != "echo after" {
		t.Fatalf("an interrupted run is cancelled, not failed-by-timeout: %+v", res)
	}

	// Its own deadline is a timeout, not a cancellation.
	r = &Runner{Cwd: root, Config: Config{Commands: []string{"sleep 30"}, Timeout: 1}}
	res = r.Run(context.Background())
	if res.Cancelled || res.Exit != 124 {
		t.Fatalf("timeout: %+v", res)
	}
}

func TestRunEachReportsNotRunWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{Cwd: t.TempDir(), Config: Config{Commands: []string{"echo a", "echo b"}}}
	res := r.RunEach(ctx).Result()
	if !res.Cancelled || len(res.NotRun) != 2 || len(res.Runs) != 0 {
		t.Fatalf("%+v", res)
	}
}
