package gitship

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// isolate keeps the user's git config (signing, hooks, templates) out of
// the test repositories.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func repo(t *testing.T) string {
	t.Helper()
	isolate(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "b.txt", "b\n")
	write(t, dir, "gone.txt", "x\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// The session's files are committed; the person's own edit, already
// staged, stays staged and out of the commit; a credential stays out.
func TestCommitTakesOnlyTheSessionsFiles(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	a := write(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	c := write(t, dir, "sub dir/c.go", "package sub\n")
	env := write(t, dir, ".env", "KEY=1\n")
	tok := write(t, dir, "conf.yaml", "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789\n")
	gone := filepath.Join(dir, "gone.txt")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "b.txt", "mine\n")
	git(t, dir, "add", "b.txt")

	plan, err := PlanCommit(ctx, filepath.Join(dir, "sub dir"), []string{a, c, env, tok, gone, "/elsewhere/x.go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.go", "gone.txt", "sub dir/c.go"}; !reflect.DeepEqual(plan.Files, want) {
		t.Fatalf("files = %v, want %v", plan.Files, want)
	}
	if !reflect.DeepEqual(plan.Others, []string{"b.txt"}) {
		t.Fatalf("others = %v", plan.Others)
	}
	if !strings.Contains(plan.Withheld[".env"], "name") || !strings.Contains(plan.Withheld["conf.yaml"], "GitHub token") {
		t.Fatalf("withheld = %v", plan.Withheld)
	}
	if !strings.Contains(plan.Diff, "func A()") || !strings.Contains(plan.Diff, "new file sub dir/c.go") {
		t.Fatalf("diff = %s", plan.Diff)
	}

	sha, err := Commit(ctx, plan.Root, plan.Files, "feat: add A and sub\n\nbody line")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "show", "--name-only", "--format=%s", sha); got != "feat: add A and sub\n\na.go\ngone.txt\nsub dir/c.go" {
		t.Fatalf("commit = %q", got)
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "b.txt" {
		t.Fatalf("still staged = %q", got)
	}
}

func TestCommitOutsideARepository(t *testing.T) {
	isolate(t)
	if _, err := PlanCommit(context.Background(), t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("err = %v", err)
	}
}

// fakeGH puts a gh on PATH that records its arguments and stdin, and
// answers pr create with a URL.
func fakeGH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1 $2" in
"pr view") exit 1 ;;
"pr create") cat > "` + log + `.body"; echo "Creating pull request"; echo "https://github.com/o/r/pull/7" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestPullRequestPushesAndOpens(t *testing.T) {
	dir := repo(t)
	log := fakeGH(t)
	ctx := context.Background()
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare")
	git(t, dir, "remote", "add", "origin", origin)
	git(t, dir, "push", "-q", "origin", "main")
	git(t, dir, "remote", "set-head", "origin", "main")

	if _, err := PlanPR(ctx, dir, ""); err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("on main: %v", err)
	}
	git(t, dir, "switch", "-q", "-c", "feature")
	if _, err := PlanPR(ctx, dir, ""); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("no commits: %v", err)
	}
	write(t, dir, "a.go", "package a\n// changed\n")
	git(t, dir, "commit", "-q", "-am", "change a")

	p, err := PlanPR(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != "main" || p.Branch != "feature" || p.Remote != "origin" || !strings.Contains(p.Log, "change a") || !strings.Contains(p.Diff, "// changed") || p.Existing != "" {
		t.Fatalf("plan = %+v", p)
	}
	url, err := OpenPR(ctx, p, "Change a", "Why it changed.")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("url = %q", url)
	}
	if got := git(t, origin, "rev-parse", "feature"); got != git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("pushed %s", got)
	}
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "pr create --base main --head feature --title Change a --body-file -") {
		t.Fatalf("gh args = %s", args)
	}
	if body, _ := os.ReadFile(log + ".body"); string(body) != "Why it changed." {
		t.Fatalf("body = %q", body)
	}
}

func TestPullRequestNeedsGH(t *testing.T) {
	dir := repo(t)
	t.Setenv("PATH", t.TempDir())
	if _, err := PlanPR(context.Background(), dir, ""); err == nil || !strings.Contains(err.Error(), "gh is not on PATH") {
		t.Fatalf("err = %v", err)
	}
}
