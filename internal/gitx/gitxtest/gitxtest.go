// Package gitxtest is the one place tests that drive a real git repository
// set that repo up so it works on a fresh machine: no user.name/user.email
// in ~/.gitconfig, no /etc/gitconfig, none of it. A CI runner has none of
// that, so a temp repo that relies on it fails with "Author identity
// unknown" the first time anything — the test itself, or code under test
// that shells out to git on its own, such as an agent's own `git commit`
// or janetcode's notes machinery — tries to commit.
//
// Repo-local config (this package's approach) is used rather than
// GIT_AUTHOR_*/GIT_COMMITTER_* environment variables because it survives
// process boundaries that an env var does not: internal/shell scrubs the
// environment of any command it runs on the agent's behalf (see
// shell.ScrubEnv), so a GIT_AUTHOR_NAME set only on the *test's* git
// commands never reaches a `git commit` the agent itself issues through
// the bash tool. A `.git/config` entry is read by every git invocation in
// that repo no matter who spawned it or what environment they were given.
package gitxtest

import (
	"os/exec"
	"testing"
)

// InitRepo runs `git init` in dir with a fixed default branch name (so
// tests do not depend on the runner's init.defaultBranch) and configures
// the repo-local identity and signing settings that let commits succeed
// with no global git config at all. Use it in place of a bare `git init`
// wherever the test, or the code under test, will commit in dir.
func InitRepo(t testing.TB, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q", "-b", "main")
	Configure(t, dir)
}

// InitBareRepo runs `git init --bare` in dir with a fixed default branch
// name. A bare repo is never committed into directly, so it needs no
// identity, but pinning the branch name keeps it consistent with clones
// made from it via InitRepo-created repos.
func InitBareRepo(t testing.TB, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q", "--bare", "-b", "main")
}

// Configure sets the repo-local identity (user.name, user.email) and
// disables commit signing on an existing repository in dir. Use it for a
// repo InitRepo did not create, most commonly one made with `git clone`.
func Configure(t testing.TB, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func runGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
