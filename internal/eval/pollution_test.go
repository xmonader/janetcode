package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func put(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPollutionCleanRunAndFixtureAreFine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	put(t, dir, ".janetcode/verify.json") // shipped by the fixture
	before := snapshotState(ctx, dir)
	put(t, dir, "main.go")
	// janetcode's own ref namespaces are by design.
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "x")
	gitIn(t, dir, "update-ref", "refs/janetcode/cp/abc", "HEAD")
	gitIn(t, dir, "update-ref", "refs/notes/janetcode", "HEAD")
	got := pollution(ctx, dir, before, true)
	if len(got) != 0 {
		t.Fatalf("polluted: %v", got)
	}
	// the default branch of the first commit is a new ref, unless allowed
	if got := pollution(ctx, dir, before, false); len(got) != 1 || !strings.Contains(got[0], "refs/heads/") {
		t.Fatalf("a new branch should be flagged: %v", got)
	}
}

func TestPollutionFlagsStateAndRefs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	before := snapshotState(ctx, dir)

	put(t, dir, ".janetcode/plan.md")
	gitIn(t, dir, "add", "-f", ".janetcode/plan.md")
	gitIn(t, dir, "tag", "v1")
	gitIn(t, dir, "branch", "side")
	got := strings.Join(pollution(ctx, dir, before, false), "\n")
	for _, want := range []string{"left .janetcode in the repo", "left .janetcode/plan.md in the repo", "committed or staged .janetcode/plan.md", "left ref refs/tags/v1", "left ref refs/heads/side"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPollutionOutsideGitOnlyChecksState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	before := snapshotState(ctx, dir)
	if before.git {
		t.Skip("temp dir is inside a repository")
	}
	put(t, dir, ".janetcode/recipes/r.json")
	if got := pollution(ctx, dir, before, false); len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestPollutedOutcomeIsAFailureOnItsOwnLine(t *testing.T) {
	r := Result{Name: "t", Polluted: []string{"left .janetcode/x in the repo"}, Reason: "left .janetcode/x in the repo"}
	if r.Outcome() != "POLLUTED" || r.Passed {
		t.Fatalf("outcome %s", r.Outcome())
	}
	s := Summarize([]Result{r, {Name: "u", Passed: true}})
	if s.Total != 2 || s.Passed != 1 || s.Polluted != 1 {
		t.Fatalf("summary %+v", s)
	}
	if !strings.Contains(summaryLine(s), "!! polluted: 1 run(s)") {
		t.Fatalf("no polluted line:\n%s", summaryLine(s))
	}
}
