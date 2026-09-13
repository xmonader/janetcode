package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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

func TestKeepWritesTaskAndPatch(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget", "wire it up")

	dir := filepath.Join(root, ".janetcode", "evals")
	path, task, err := Keep(KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir})
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
	if _, _, err := Keep(KeepOptions{Root: root, Session: sess, Name: task.Name, Check: "true", Dir: dir}); err == nil {
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
	repo, err := snapshot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget")

	dir := filepath.Join(root, ".janetcode", "evals")
	_, task, err := Keep(KeepOptions{Root: root, Session: sess, Check: "true", Dir: dir})
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
		arg, name, check string
		wantErr          bool
	}{
		{"", "", "", false},
		{"my-fix", "my-fix", "", false},
		{"my fix", "my fix", "", false},
		{"--check go test ./...", "", "go test ./...", false},
		{"my-fix --check go test ./...", "my-fix", "go test ./...", false},
		{"my-fix --check", "", "", true},
	}
	for _, c := range cases {
		name, check, err := ParseKeepArgs(c.arg)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseKeepArgs(%q): err = %v, wantErr %v", c.arg, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if name != c.name || check != c.check {
			t.Errorf("ParseKeepArgs(%q) = (%q, %q), want (%q, %q)", c.arg, name, check, c.name, c.check)
		}
	}
}
