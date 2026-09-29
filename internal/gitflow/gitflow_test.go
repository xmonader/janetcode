package gitflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gitEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.go", "package a\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: first")
	return dir
}

func TestPendingWholeTreeHoldsCredentialFiles(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	write(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	write(t, dir, "b.go", "package a\n")
	write(t, dir, ".env", "TOKEN=abc\n")
	write(t, dir, "certs/server.pem", "x\n")
	c, err := Pending(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Staged {
		t.Fatal("nothing was staged")
	}
	got := strings.Join(c.Files, ",")
	if !strings.Contains(got, "M\ta.go") || !strings.Contains(got, "A\tb.go") || strings.Contains(got, ".env") || strings.Contains(got, "pem") {
		t.Fatalf("files = %q", got)
	}
	if len(c.Held) != 2 {
		t.Fatalf("held = %v", c.Held)
	}
	sha, err := Commit(ctx, dir, "feat: add A and b")
	if err != nil {
		t.Fatal(err)
	}
	if git(t, dir, "rev-parse", "HEAD") != sha {
		t.Fatal("returned sha is not HEAD")
	}
	files := git(t, dir, "show", "--name-only", "--format=", "HEAD")
	if files != "a.go\nb.go" {
		t.Fatalf("committed %q", files)
	}
	if st := git(t, dir, "status", "--porcelain"); !strings.Contains(st, "?? .env") {
		t.Fatalf(".env should still be untracked: %q", st)
	}
}

func TestPendingStagedOnly(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	write(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	write(t, dir, "b.go", "package a\n")
	git(t, dir, "add", "b.go")
	c, err := Pending(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Staged || len(c.Files) != 1 || c.Files[0] != "A\tb.go" {
		t.Fatalf("change = %+v", c)
	}
	if _, err := Commit(ctx, dir, "feat: b"); err != nil {
		t.Fatal(err)
	}
	if files := git(t, dir, "show", "--name-only", "--format=", "HEAD"); files != "b.go" {
		t.Fatalf("committed %q, want only the staged file", files)
	}
	if st := git(t, dir, "status", "--porcelain"); !strings.Contains(st, "M a.go") {
		t.Fatalf("a.go's unstaged change should remain: %q", st)
	}
}

func TestCommitNothing(t *testing.T) {
	dir := repo(t)
	if _, err := Commit(context.Background(), dir, "x"); err == nil || !strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Commit(context.Background(), dir, "  "); err == nil {
		t.Fatal("empty message committed")
	}
}

func TestPendingFlagsAddedSecretAndRedactsPrompt(t *testing.T) {
	dir := repo(t)
	key := "sk-ant-api03-" + strings.Repeat("Ab3xQ9", 12)
	write(t, dir, "cfg.go", "package a\n\nconst k = \""+key+"\"\n")
	c, err := Pending(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Secret == "" || !strings.Contains(c.Preview(), "warning") {
		t.Fatalf("secret not flagged: %q", c.Preview())
	}
	if strings.Contains(CommitPrompt(c, nil), key) {
		t.Fatal("the drafting prompt carries the key")
	}
}

func TestCommitPromptCarriesStyle(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.go", "package a\n// x\n")
	c, _ := Pending(context.Background(), dir)
	p := CommitPrompt(c, RecentSubjects(context.Background(), dir, 5))
	if !strings.Contains(p, "feat: first") || !strings.Contains(p, "+// x") {
		t.Fatalf("prompt = %q", p)
	}
}

func TestCleanMessage(t *testing.T) {
	for in, want := range map[string]string{
		"```\nfix: x\n\nbody\n```": "fix: x\n\nbody",
		"```text\nfix: x\n```":     "fix: x",
		"Commit message: fix: y":   "fix: y",
		"\"fix: z\"":               "fix: z",
		"  fix: plain  \n":         "fix: plain",
	} {
		if got := CleanMessage(in); got != want {
			t.Errorf("CleanMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareURL(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:acme/tool.git":           "https://github.com/acme/tool/compare/main...feat?expand=1",
		"https://github.com/acme/tool":           "https://github.com/acme/tool/compare/main...feat?expand=1",
		"https://x:tok@github.com/acme/tool.git": "https://github.com/acme/tool/compare/main...feat?expand=1",
		"ssh://git@github.com/acme/tool.git":     "https://github.com/acme/tool/compare/main...feat?expand=1",
		"/tmp/bare.git":                          "",
		"git@gitlab.com:acme/tool.git":           "",
	} {
		if got := CompareURL(remote, "main", "feat"); got != want {
			t.Errorf("CompareURL(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestParseDraft(t *testing.T) {
	title, body := ParseDraft("TITLE: Add retries\n\nRetries the call.\n\nTested with go test.")
	if title != "Add retries" || body != "Retries the call.\n\nTested with go test." {
		t.Fatalf("%q / %q", title, body)
	}
	title, body = ParseDraft("Add retries")
	if title != "Add retries" || body != "" {
		t.Fatalf("%q / %q", title, body)
	}
}

// withRemote is a repository with a bare origin whose main is pushed, on
// a feature branch one commit ahead.
func withRemote(t *testing.T) (dir, bare string) {
	dir = repo(t)
	bare = filepath.Join(t.TempDir(), "origin.git")
	git(t, dir, "init", "-q", "--bare", bare)
	git(t, dir, "remote", "add", "origin", bare)
	git(t, dir, "push", "-q", "origin", "main")
	git(t, dir, "checkout", "-q", "-b", "feat/x")
	write(t, dir, "a.go", "package a\n\nfunc X() {}\n")
	git(t, dir, "commit", "-q", "-am", "feat: X")
	return dir, bare
}

func TestPlanPR(t *testing.T) {
	GH = "janetcode-no-such-gh"
	t.Cleanup(func() { GH = "gh" })
	dir, _ := withRemote(t)
	p, err := PlanPR(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Branch != "feat/x" || p.Base != "main" || p.Remote != "origin" || len(p.Commits) != 1 || p.GH {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(PRPrompt(p), "feat: X") {
		t.Fatal("prompt lacks the commit")
	}
	git(t, dir, "checkout", "-q", "main")
	if _, err := PlanPR(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("on main: %v", err)
	}
}

func TestCreatePRWithoutGHPushesAndNeedsGitHubForURL(t *testing.T) {
	GH = "janetcode-no-such-gh"
	t.Cleanup(func() { GH = "gh" })
	dir, bare := withRemote(t)
	_, err := CreatePR(context.Background(), dir, "Add X", "")
	if err == nil || !strings.Contains(err.Error(), "open the pull request by hand") {
		t.Fatalf("err = %v", err)
	}
	if git(t, bare, "rev-parse", "feat/x") != git(t, dir, "rev-parse", "HEAD") {
		t.Fatal("branch was not pushed")
	}
}

// fakeGH writes a gh that logs its arguments and stdin and answers from
// canned files.
func fakeGH(t *testing.T) (logFile string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	d := t.TempDir()
	logFile = filepath.Join(d, "log")
	script := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1 $2" in
"auth status") exit 0;;
"pr create") cat >> ` + logFile + `; echo "https://github.com/acme/tool/pull/7";;
"pr view") echo '{"number":7,"title":"Add X","url":"https://github.com/acme/tool/pull/7","state":"OPEN","body":"desc","comments":[{"author":{"login":"ann"},"body":"nice"}],"reviews":[{"author":{"login":"bob"},"state":"CHANGES_REQUESTED","body":"fix the loop"}]}';;
"api repos/{owner}/{repo}/pulls/7/comments") echo '[{"user":{"login":"bob"},"path":"a.go","line":3,"body":"off by one"}]';;
"issue view") echo '{"number":12,"title":"Crash","url":"u","state":"OPEN","body":"it crashes","comments":[],"labels":[{"name":"bug"}]}';;
*) echo "unexpected $*" >&2; exit 1;;
esac
`
	p := filepath.Join(d, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	GH = p
	t.Cleanup(func() { GH = "gh" })
	return logFile
}

func TestCreatePRWithGH(t *testing.T) {
	log := fakeGH(t)
	dir, bare := withRemote(t)
	res, err := CreatePR(context.Background(), dir, "Add X", "Body text")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Opened || res.URL != "https://github.com/acme/tool/pull/7" {
		t.Fatalf("res = %+v", res)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "pr create --base main --head feat/x --title Add X --body-file -") || !strings.Contains(string(b), "Body text") {
		t.Fatalf("gh log = %s", b)
	}
	if git(t, bare, "rev-parse", "feat/x") == "" {
		t.Fatal("not pushed")
	}
}

func TestPRCommentsAndIssue(t *testing.T) {
	fakeGH(t)
	dir := repo(t)
	got, err := PRComments(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#7: Add X", "Review by bob (changes requested):\nfix the loop", "a.go:3, bob:\noff by one", "Comment by ann:\nnice"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	is, err := Issue(context.Background(), dir, "#12")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(is, "Issue #12: Crash") || !strings.Contains(is, "labels: bug") {
		t.Fatalf("issue = %s", is)
	}
}
