package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// initKeepRepo makes a small git repository with one commit, then leaves
// it with an uncommitted modification and an untracked file, standing in
// for the state a session found the project in when it started.
func initKeepRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	// Uncommitted at "session start": a tracked file changed and a new
	// untracked file, exactly what /eval keep's patch needs to reproduce.
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\n// widget added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "widget.go"), []byte("package main\n\nfunc Widget() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// keptSession builds a *session.Session with a recorded Start state and
// the given user turns logged in order, the way real turns would be.
func keptSession(t *testing.T, root string, snap snapshot.Snap, prompts ...string) *session.Session {
	t.Helper()
	sess, err := session.New(t.TempDir(), root, "anthropic", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range prompts {
		turn := i + 1
		msg := llm.TextMessage(llm.RoleUser, p)
		if _, err := sess.Append(session.Event{Type: session.TypeUserMessage, Turn: turn, Surface: true, Message: &msg}); err != nil {
			t.Fatal(err)
		}
		reply := llm.TextMessage(llm.RoleAssistant, "ok")
		if _, err := sess.Append(session.Event{Type: session.TypeAssistant, Turn: turn, Surface: true, Message: &reply}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.SetStart(session.StartState{Head: snap.Head, Snap: snap.Commit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return sess
}

// runGit runs a git command in dir for a test, failing it on error and
// returning trimmed stdout+stderr.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestKeepWritesTaskAndPatch(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget", "wire it up")

	dir := filepath.Join(root, ".janetcode", "evals")
	path, task, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if path != filepath.Join(dir, task.Name+".json") {
		t.Fatalf("path = %s, want %s/%s.json", path, dir, task.Name)
	}
	if got, want := task.Prompts, []string{"add a widget", "wire it up"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Prompts = %v, want %v", got, want)
	}
	if task.Base != snap.Head {
		t.Fatalf("Base = %s, want %s", task.Base, snap.Head)
	}
	if task.Check != "true" {
		t.Fatalf("Check = %q", task.Check)
	}
	if task.Patch == "" {
		t.Fatal("Patch is empty; the session started with uncommitted changes")
	}
	if _, err := os.Stat(filepath.Join(dir, task.Patch)); err != nil {
		t.Fatalf("patch file: %v", err)
	}
	if resolved := filepath.Clean(task.RepoPath()); resolved != filepath.Clean(root) {
		t.Fatalf("RepoPath() = %s, want %s", resolved, root)
	}

	// A second Keep with the same name must refuse to overwrite.
	if _, _, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Name: task.Name, Check: "true", Dir: dir}); err == nil {
		t.Fatal("Keep did not refuse to overwrite an existing task")
	}
}

// TestPrepareWorktreeMatchesSnapshot is the point of the feature: the work
// dir a kept task prepares must be the tree the session actually started
// from — Base plus Patch — not an empty directory. It is tested here
// directly, without a model, by calling the preparation function Keep's
// task is designed to feed.
func TestPrepareWorktreeMatchesSnapshot(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget")

	dir := filepath.Join(root, ".janetcode", "evals")
	_, task, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}

	work, cleanup, err := prepareWorktree(context.Background(), task)
	if err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	defer cleanup()

	got, err := os.ReadFile(filepath.Join(work, "main.go"))
	if err != nil {
		t.Fatalf("main.go: %v", err)
	}
	if string(got) != "package main\n\n// widget added\n" {
		t.Fatalf("main.go = %q, want the uncommitted edit", got)
	}
	got, err = os.ReadFile(filepath.Join(work, "widget.go"))
	if err != nil {
		t.Fatalf("widget.go: %v", err)
	}
	if string(got) != "package main\n\nfunc Widget() {}\n" {
		t.Fatalf("widget.go = %q, want the untracked file", got)
	}

	// The worktree is registered with git and gone once removed.
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("worktree missing before cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Stat(work); err == nil {
		t.Fatal("worktree still on disk after cleanup")
	}
}

func TestParseKeepArgs(t *testing.T) {
	cases := []struct {
		arg                    string
		name, check, base      string
		selfContained, wantErr bool
	}{
		{arg: "", name: "", check: "", base: ""},
		{arg: "my-fix", name: "my-fix"},
		{arg: "my fix", name: "my fix"},
		{arg: "--check go test ./...", check: "go test ./..."},
		{arg: "my-fix --check go test ./...", name: "my-fix", check: "go test ./..."},
		{arg: "my-fix --check", wantErr: true},
		{arg: "--base abc123", base: "abc123"},
		{arg: "my-fix --base abc123", name: "my-fix", base: "abc123"},
		{arg: "my-fix --base abc123 --check go test ./...", name: "my-fix", base: "abc123", check: "go test ./..."},
		{arg: "my-fix --base", wantErr: true},
		{arg: "--self-contained", selfContained: true},
		{arg: "my-fix --self-contained --base abc123", name: "my-fix", base: "abc123", selfContained: true},
	}
	for _, c := range cases {
		name, check, base, selfContained, err := ParseKeepArgs(c.arg)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseKeepArgs(%q): err = %v, wantErr %v", c.arg, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if name != c.name || check != c.check || base != c.base || selfContained != c.selfContained {
			t.Errorf("ParseKeepArgs(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)", c.arg, name, check, base, selfContained, c.name, c.check, c.base, c.selfContained)
		}
	}
}

// TestKeepUnbornRepo covers /eval keep's first limitation: a session that
// began in a repository with no commits yet. Base must stay empty and
// the whole starting tree must travel as a patch against the empty tree,
// and preparing the work dir from that task must reproduce the tree
// snapshot.Take recorded, exactly as a Base-plus-Patch task would.
func TestKeepUnbornRepo(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "widget.go"), []byte("package main\n\nfunc Widget() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "unborn start")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Head != "" {
		t.Fatalf("Head = %q, want empty on an unborn branch", snap.Head)
	}
	sess := keptSession(t, root, snap, "start the project")

	dir := filepath.Join(root, ".janetcode", "evals")
	_, task, warnings, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if task.Base != "" {
		t.Fatalf("Base = %q, want empty for an unborn repository", task.Base)
	}
	if task.Patch == "" {
		t.Fatal("want a patch carrying the whole starting tree")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none: there is no base commit to be missing from a remote", warnings)
	}

	work, cleanup, err := prepareWorkDir(context.Background(), task)
	if err != nil {
		t.Fatalf("prepareWorkDir: %v", err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(work, "main.go"))
	if err != nil {
		t.Fatalf("main.go: %v", err)
	}
	if string(got) != "package main\n" {
		t.Fatalf("main.go = %q", got)
	}
	got, err = os.ReadFile(filepath.Join(work, "widget.go"))
	if err != nil {
		t.Fatalf("widget.go: %v", err)
	}
	if string(got) != "package main\n\nfunc Widget() {}\n" {
		t.Fatalf("widget.go = %q", got)
	}
}

// TestKeepWithBaseOverride covers a session with no recorded Start (one
// begun before this feature existed, or where recording failed): Keep
// must refuse without --base and name it as the way out, and with
// --base must use that commit as Base with no patch, even though the
// repository has uncommitted changes /eval keep would otherwise diff.
func TestKeepWithBaseOverride(t *testing.T) {
	root := initKeepRepo(t)
	head := runGit(t, root, "rev-parse", "HEAD")

	sess, err := session.New(t.TempDir(), root, "anthropic", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "fix the bug")
	if _, err := sess.Append(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	// Deliberately no SetStart: this session predates start recording.

	dir := filepath.Join(root, ".janetcode", "evals")
	if _, _, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Name: "no-base", Check: "true", Dir: dir}); err == nil {
		t.Fatal("Keep without Start or --base must fail")
	} else if !strings.Contains(err.Error(), "--base") {
		t.Fatalf("error = %v, want it to name --base as the way out", err)
	}

	_, task, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Name: "with-base", Base: head, Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep with --base: %v", err)
	}
	if task.Base != head {
		t.Fatalf("Base = %s, want %s", task.Base, head)
	}
	if task.Patch != "" {
		t.Fatalf("Patch = %q, want none: --base has no snapshot to diff against", task.Patch)
	}
}

// TestKeepSelfContained covers --self-contained: Base stays empty and the
// whole tree at the session's snapshot travels as a patch, so the task
// still prepares correctly once the source repository is gone entirely.
func TestKeepSelfContained(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "self-contained start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget")

	dir := t.TempDir()
	_, task, _, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir, SelfContained: true})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if task.Base != "" {
		t.Fatalf("Base = %q, want empty for a self-contained task", task.Base)
	}
	if task.Patch == "" {
		t.Fatal("want a patch carrying the whole starting tree")
	}

	// The task must run without the source repository existing at all.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	work, cleanup, err := prepareWorkDir(context.Background(), task)
	if err != nil {
		t.Fatalf("prepareWorkDir: %v", err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(work, "main.go"))
	if err != nil {
		t.Fatalf("main.go: %v", err)
	}
	if string(got) != "package main\n\n// widget added\n" {
		t.Fatalf("main.go = %q", got)
	}
	if _, err := os.ReadFile(filepath.Join(work, "widget.go")); err != nil {
		t.Fatalf("widget.go: %v", err)
	}
}

// TestKeepBaseWarnings covers /eval keep's third limitation: a Base
// commit may exist only in the clone that kept it. The warning must be
// present when the base is unpushed and gone once it is on a remote.
func TestKeepBaseWarnings(t *testing.T) {
	root := initKeepRepo(t)
	remote := t.TempDir()
	runGit(t, remote, "init", "-q", "--bare")
	runGit(t, root, "remote", "add", "origin", remote)

	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "warning start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "do a thing")

	dir := filepath.Join(root, ".janetcode", "evals")
	_, _, warnings, err := Keep(context.Background(), KeepOptions{Root: root, Session: sess, Name: "local-only", Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("want a warning: the base commit is not on any remote yet")
	}

	branch := runGit(t, root, "branch", "--show-current")
	if branch == "" {
		branch = "master"
	}
	runGit(t, root, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	runGit(t, root, "fetch", "-q", "origin")

	_, _, warnings, err = Keep(context.Background(), KeepOptions{Root: root, Session: sess, Name: "pushed", Check: "true", Dir: dir})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none once the base is on a remote", warnings)
	}
}

// TestPrepareWorktreeMissingBase covers /eval keep's fourth limitation: a
// Base commit that is not in the clone running the task must fail with a
// clear error, not a raw git message.
func TestPrepareWorktreeMissingBase(t *testing.T) {
	root := initKeepRepo(t)
	task := Task{Name: "missing-base", Repo: root, Base: strings.Repeat("a", 40)}
	if _, _, err := prepareWorktree(context.Background(), task); err == nil {
		t.Fatal("want an error for a base commit that does not exist")
	} else if !strings.Contains(err.Error(), "is not in this repository") || !strings.Contains(err.Error(), "--self-contained") {
		t.Fatalf("error = %v, want it to name the fix", err)
	}
}

// TestKeepNotAGitRepo is the regression test for the third QA finding:
// Keep needs a git repository for every path it takes (the check
// command's default source, the base commit, the diff), so a Root that
// is not one must fail with one clear message up front — not the
// misleading "no check command" (when neither --check nor --base was
// given either) and not a raw `git` fatal error surfacing from --base's
// own git command.
func TestKeepNotAGitRepo(t *testing.T) {
	root := t.TempDir() // deliberately not a git repository
	sess, err := session.New(t.TempDir(), root, "anthropic", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "do something")
	if _, err := sess.Append(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &msg}); err != nil {
		t.Fatal(err)
	}

	_, _, _, err = Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true"})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("Keep with a check command but no git repo: err = %v, want \"not a git repository\"", err)
	}

	// With no --check either, the missing-repository message must win
	// over "no check command": the repository is the more fundamental
	// problem, and naming the check command would not fix it.
	_, _, _, err = Keep(context.Background(), KeepOptions{Root: root, Session: sess})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "no check command") {
		t.Fatalf("Keep with neither check nor git repo: err = %v, want only \"not a git repository\"", err)
	}

	// --base in a non-git directory must give that same friendly
	// message, not resolveRev's raw `git rev-parse ... fatal:` output.
	_, _, _, err = Keep(context.Background(), KeepOptions{Root: root, Session: sess, Check: "true", Base: "HEAD"})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("Keep --base in a non-git dir: err = %v, want the friendly message, not a raw git fatal", err)
	}
}

// TestKeptPromptExcludesInjectedContext is the regression test for the
// second QA bug: skill.TurnPrep appended recalled-memory (and skill)
// blocks, and repomap.TurnPrep appended the repo-map block, to the user
// turn without Synthetic: true — so eval.Keep's userPrompts (which only
// keeps non-synthetic text, see messageText) picked them up and baked
// memory entries and the repo map into the committed task file. This
// drives the real TurnPrep hooks, the same way agent.RunBlocks does, and
// checks that a kept prompt is exactly the text the user typed.
func TestKeptPromptExcludesInjectedContext(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.go": "package m\n\nfunc A() {}\n",
		"b.go": "package m\n\nfunc B() { A() }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mem := memory.Open(t.TempDir(), t.TempDir())
	mem.Put(memory.Entry{Name: "goose", Description: "Database migrations use goose", Body: "goose up from db/"})
	skillInj := &skill.Injector{Skills: func() []skill.Skill {
		return []skill.Skill{{Name: "sql-migrations", Description: "Write database schema migrations", Body: "steps", ModelInvocable: true}}
	}}
	repoCache := &repomap.Cache{Root: root}

	const typed = "add a database schema migration for users"
	blocks := []llm.Block{{Type: llm.BlockText, Text: typed}}
	blocks, note := skill.TurnPrep(skillInj, mem)(blocks)
	if note == "" {
		t.Fatalf("test setup: skill.TurnPrep attached nothing")
	}
	blocks, note = repomap.TurnPrep(repoCache, 0)(blocks)
	if note == "" {
		t.Fatalf("test setup: repomap.TurnPrep attached nothing")
	}
	// Sanity check that this test actually exercises the bug: several
	// blocks of injected context, beyond the one the user typed.
	if len(blocks) < 3 {
		t.Fatalf("test setup only produced %d blocks: %+v", len(blocks), blocks)
	}

	msg := llm.Message{Role: llm.RoleUser, Blocks: blocks}
	events := []session.Event{{Type: session.TypeUserMessage, Turn: 1, Message: &msg}}

	got := userPrompts(events)
	if len(got) != 1 || got[0] != typed {
		t.Fatalf("userPrompts = %v, want [%q]", got, typed)
	}
}

// TestParseKeepArgsCheckInsideName: --check is only the flag as a whole
// word. It used to be found anywhere in the text, so a task name that
// merely contained "--check" was cut in two and its tail taken as the
// check command.
func TestParseKeepArgsCheckInsideName(t *testing.T) {
	name, check, _, _, err := ParseKeepArgs("fix--checkout --check go test ./...")
	if err != nil || name != "fix--checkout" || check != "go test ./..." {
		t.Fatalf("got name=%q check=%q err=%v", name, check, err)
	}
	name, check, _, _, err = ParseKeepArgs("fix--checkout")
	if err != nil || name != "fix--checkout" || check != "" {
		t.Fatalf("got name=%q check=%q err=%v", name, check, err)
	}
}
