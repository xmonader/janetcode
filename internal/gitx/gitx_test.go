package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
)

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com"}
	ctx := context.Background()
	// A repo-local identity (rather than relying on the env overrides
	// below for every git invocation) means TestRunEnv's own partial
	// override -- GIT_AUTHOR_NAME alone -- still resolves a valid email
	// from config instead of git falling back to guessing one from the
	// machine's hostname, which is not always resolvable to a valid
	// address.
	gitxtest.InitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "first"}} {
		if _, err := Run(ctx, dir, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunTrimsTrailingNewlinesOnly(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	head, err := Run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(head) < 40 || strings.ContainsAny(head, "\n ") {
		t.Fatalf("head = %q", head)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Porcelain status starts with a space for an unstaged change: Run
	// must keep it.
	st, err := Run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if st != " M a.txt" {
		t.Fatalf("status = %q, want leading space kept and trailing newline gone", st)
	}
	raw, err := RunBytes(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != " M a.txt\n" {
		t.Fatalf("RunBytes = %q, want untouched output", raw)
	}
}

func TestRunEnv(t *testing.T) {
	dir := repo(t)
	out, err := Run(context.Background(), dir, []string{"GIT_AUTHOR_NAME=someone-else"}, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "someone-else ") {
		t.Fatalf("ident = %q, want the env override", out)
	}
}

func TestErrorCarriesStderr(t *testing.T) {
	dir := repo(t)
	_, err := Run(context.Background(), dir, nil, "rev-parse", "--verify", "no-such-ref")
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v (%T), want *Error", err, err)
	}
	if !strings.HasPrefix(err.Error(), "git rev-parse: ") || ge.Stderr == "" {
		t.Fatalf("err = %q, stderr = %q", err, ge.Stderr)
	}
	if ExitCode(err) <= 0 || Detail(err) != ge.Stderr {
		t.Fatalf("exit = %d, detail = %q", ExitCode(err), Detail(err))
	}
	if ExitCode(nil) != -1 || ExitCode(errors.New("x")) != -1 || Detail(errors.New("x")) != "x" {
		t.Fatal("non-git errors must not look like git exits")
	}
}

func TestCommitExists(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	if err := CommitExists(ctx, dir, "HEAD"); err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if err := CommitExists(ctx, dir, strings.Repeat("a", 40)); err == nil {
		t.Fatal("a made-up sha exists")
	}
}

// Diff from the empty tree recreates the whole tree; Apply puts it back.
func TestDiffAndApply(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	patch, err := Diff(ctx, dir, "", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+one") {
		t.Fatalf("patch = %q", patch)
	}
	dst := t.TempDir()
	if _, err := Run(ctx, dst, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, dst, patch); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "one\n" {
		t.Fatalf("a.txt = %q, %v", b, err)
	}
	if err := Apply(ctx, dst, []byte("not a patch\n")); err == nil {
		t.Fatal("garbage applied")
	}
}

func TestShort(t *testing.T) {
	if Short("0123456789abcdef") != "0123456789ab" || Short("abc") != "abc" {
		t.Fatal("Short")
	}
}

func TestWrapfKeepsTextAndChain(t *testing.T) {
	ge := &Error{Args: []string{"notes", "add"}, Stderr: "fatal: nope", Err: errors.New("exit status 128")}
	err := Wrapf(ge, "git notes add %s", "x")
	if got, want := err.Error(), "git notes add x: fatal: nope"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	var back *Error
	if !errors.As(err, &back) || back != ge {
		t.Fatal("Wrapf dropped the *Error from the chain")
	}
	if Detail(err) != "fatal: nope" {
		t.Fatalf("Detail through Wrapf = %q", Detail(err))
	}
}
