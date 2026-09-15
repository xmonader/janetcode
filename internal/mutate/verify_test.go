package mutate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/verify"
)

func TestVerifyMutationGateAfterCommandsPass(t *testing.T) {
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
	r := &verify.Runner{Mutate: ForVerify, Cwd: root, Config: verify.Config{Commands: []string{"true"}, Mutate: "true", MaxMutants: 3}}
	res := r.Run(context.Background())
	if res.OK() || res.Failed != "" || res.Mutation == nil || len(res.Mutation.(Report).Survived) == 0 {
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
	if !res.OK() || res.Mutation == nil || res.Mutation.(Report).Killed != res.Mutation.(Report).Total || !strings.Contains(res.Summary(), "mutants killed") {
		t.Fatalf("res = %+v summary %q", res, res.Summary())
	}
	// Mutate alone enables the runner; outside git it reports, not fails.
	r = &verify.Runner{Mutate: ForVerify, Cwd: t.TempDir(), Config: verify.Config{Mutate: "true"}}
	if !r.Enabled() {
		t.Fatal("mutate-only config not enabled")
	}
	if res := r.Run(context.Background()); !res.OK() || res.MutationErr == nil || !strings.Contains(res.Summary(), "mutation testing unavailable") {
		t.Fatalf("outside git: %+v", res)
	}
}

// A runner built without a MutateFunc reports mutation testing as
// unavailable instead of silently skipping it.
func TestVerifyWithoutMutator(t *testing.T) {
	r := &verify.Runner{Cwd: t.TempDir(), Config: verify.Config{Mutate: "true"}}
	if res := r.Run(context.Background()); !res.OK() || res.MutationErr == nil || !strings.Contains(res.Summary(), "mutation testing unavailable") {
		t.Fatalf("no mutator: %+v", res)
	}
}
