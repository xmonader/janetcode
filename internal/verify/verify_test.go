package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestMutationGateAfterCommandsPass(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc Pos(n int) bool { return n > 0 }\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc Pos(n int) bool { return n > 0 }\n\nfunc Neg(n int) bool { return n < 0 }\n"), 0o644)
	// "true" as the test command catches nothing: every mutant survives.
	r := &Runner{Cwd: root, Config: Config{Commands: []string{"true"}, Mutate: "true", MaxMutants: 3}}
	res := r.Run(context.Background())
	if res.OK() || res.Failed != "" || res.Mutation == nil || len(res.Mutation.Survived) == 0 {
		t.Fatalf("res = %+v", res)
	}
	if !strings.HasPrefix(res.Summary(), "verification failed: mutation testing:") || !strings.Contains(res.Feedback(), "do not catch") {
		t.Fatalf("summary %q feedback %q", res.Summary(), res.Feedback())
	}
	// A failing command short-circuits: no mutation run.
	r.Config.Commands = []string{"false"}
	if res := r.Run(context.Background()); res.Mutation != nil || res.Failed != "false" {
		t.Fatalf("res = %+v", res)
	}
	// "false" as the test command kills everything.
	r.Config.Commands, r.Config.Mutate = []string{"true"}, "false"
	res = r.Run(context.Background())
	if !res.OK() || res.Mutation == nil || res.Mutation.Killed != res.Mutation.Total || !strings.Contains(res.Summary(), "mutants killed") {
		t.Fatalf("res = %+v summary %q", res, res.Summary())
	}
	// Mutate alone enables the runner; outside git it reports, not fails.
	r = &Runner{Cwd: t.TempDir(), Config: Config{Mutate: "true"}}
	if !r.Enabled() {
		t.Fatal("mutate-only config not enabled")
	}
	if res := r.Run(context.Background()); !res.OK() || res.MutationErr == nil || !strings.Contains(res.Summary(), "mutation testing unavailable") {
		t.Fatalf("outside git: %+v", res)
	}
}
