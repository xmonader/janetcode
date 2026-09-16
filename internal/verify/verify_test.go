package verify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadSaveAndHash(t *testing.T) {
	root := t.TempDir()
	if c, h, err := Load(root); err != nil || h != "" || len(c.Commands) != 0 {
		t.Fatalf("missing file: %+v %q %v", c, h, err)
	}
	if err := Save(root, Config{Commands: []string{"go build ./...", "go test ./..."}, Timeout: 30}); err != nil {
		t.Fatal(err)
	}
	c, h, err := Load(root)
	if err != nil || h == "" || len(c.Commands) != 2 || c.Timeout != 30 {
		t.Fatalf("Load = %+v %q %v", c, h, err)
	}
	os.WriteFile(filepath.Join(root, ProjectFile), []byte(`{"commands":["true",""]}`), 0o644)
	if _, _, err := Load(root); err == nil || !strings.Contains(err.Error(), "command 2 is empty") {
		t.Fatalf("empty command accepted: %v", err)
	}
	os.WriteFile(filepath.Join(root, ProjectFile), []byte(`{`), 0o644)
	if _, _, err := Load(root); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestRunStopsAtFirstFailure(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Cwd: root, Config: Config{Commands: []string{"echo one > one.txt", "echo boom >&2; exit 3", "echo never > never.txt"}}}
	res := r.Run(context.Background())
	if res.OK() || res.Failed != "echo boom >&2; exit 3" || res.Exit != 3 || !strings.Contains(res.Output, "boom") {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Passed) != 1 {
		t.Fatalf("passed = %v", res.Passed)
	}
	if _, err := os.Stat(filepath.Join(root, "never.txt")); err == nil {
		t.Fatal("third command ran after a failure")
	}
	if !strings.Contains(res.Feedback(), "Exit code: 3") || !strings.Contains(res.Feedback(), "boom") {
		t.Fatalf("feedback = %q", res.Feedback())
	}
	if !strings.HasPrefix(res.Summary(), "verification failed: echo boom") {
		t.Fatalf("summary = %q", res.Summary())
	}
	if os.Getenv("JANETCODE_VERIFY") != "" {
		t.Fatal("env leaked into the test process")
	}
}

func TestRunPassAndTimeout(t *testing.T) {
	r := &Runner{Cwd: t.TempDir(), Config: Config{Commands: []string{"test \"$JANETCODE_VERIFY\" = 1", "true"}}}
	res := r.Run(context.Background())
	if !res.OK() || len(res.Passed) != 2 || !strings.HasPrefix(res.Summary(), "verified: ") {
		t.Fatalf("res = %+v", res)
	}
	r = &Runner{Cwd: t.TempDir(), Config: Config{Commands: []string{"sleep 5"}, Timeout: 1}}
	start := time.Now()
	res = r.Run(context.Background())
	if res.OK() || res.Exit != 124 || !strings.Contains(res.Output, "timed out") || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout res = %+v after %s", res, time.Since(start))
	}
	var nilRunner *Runner
	if nilRunner.Enabled() || (&Runner{}).Enabled() {
		t.Fatal("empty runner reported enabled")
	}
}

// The timeout has to bound the whole process tree: a check that leaves
// something behind holding its output pipes (a dev server, a stray
// watcher) used to keep the gate — and the turn — waiting long after the
// deadline, because only the shell was killed.
func TestTimeoutKillsTheWholeTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell command")
	}
	r := &Runner{Cwd: t.TempDir(), Config: Config{Commands: []string{"sleep 60 & wait"}, Timeout: 1}}
	start := time.Now()
	res := r.Run(context.Background())
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("verify ran for %s despite a 1s timeout", elapsed)
	}
	if res.OK() || res.Exit != 124 {
		t.Fatalf("res = %+v", res)
	}
}

// A check's output is bounded: a runaway build must not be held in
// memory in full when only a few thousand bytes of it are ever shown.
func TestOutputIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell command")
	}
	r := &Runner{Cwd: t.TempDir(), Config: Config{
		Commands: []string{"head -c 4000000 /dev/zero | tr '\\0' 'x'; exit 1"},
		Timeout:  60,
	}}
	res := r.Run(context.Background())
	if res.Exit != 1 {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Output) > 2*maxEnd+1000 {
		t.Fatalf("kept %d bytes of output, cap is %d of each end", len(res.Output), maxEnd)
	}
	if !strings.Contains(res.Output, "bytes omitted") {
		t.Fatal("the truncation is not explained")
	}
}
