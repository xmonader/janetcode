// Package gitx is the one way the rest of janetcode runs git: a command in
// a directory, stdout back, and on failure an error that carries git's own
// stderr. It is a leaf package (standard library only) so every package
// that shells out to git — snapshot, proof, eval, worktree, mutate, the
// front ends — can share it without an import cycle.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Error is a git command that failed. Its message is "git <subcommand>:
// <stderr>", falling back to the process error when git printed nothing.
type Error struct {
	// Args is the full argument list git was given.
	Args []string
	// Stderr is what git printed on stderr, trimmed.
	Stderr string
	// Err is the underlying error from os/exec (usually *exec.ExitError).
	Err error
}

func (e *Error) Error() string {
	sub := ""
	if len(e.Args) > 0 {
		sub = e.Args[0]
	}
	return fmt.Sprintf("git %s: %s", sub, e.Detail())
}

// Detail is git's own explanation: its stderr, or the process error when
// stderr was empty.
func (e *Error) Detail() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode is git's exit status, or -1 when it did not run to an exit.
func (e *Error) ExitCode() int {
	var ee *exec.ExitError
	if errors.As(e.Err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Detail is git's own explanation for err when err is an *Error (see
// Error.Detail), and err's text otherwise.
func Detail(err error) string {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Detail()
	}
	return err.Error()
}

// Wrapf prefixes err with a formatted context the way
// fmt.Errorf(format+": %s", args..., Detail(err)) would, but keeps err in
// the chain, so errors.As still finds the *Error and ExitCode still works.
func Wrapf(err error, format string, args ...any) error {
	return &wrapped{msg: fmt.Sprintf(format, args...) + ": " + Detail(err), err: err}
}

type wrapped struct {
	msg string
	err error
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }

// ExitCode is err's git exit status when err is an *Error that exited, -1
// otherwise (including err == nil).
func ExitCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.ExitCode()
	}
	return -1
}

// Run runs git args in dir with env appended to the process environment,
// and returns stdout with trailing newlines removed (leading whitespace is
// kept: porcelain formats put status columns there).
func Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := run(ctx, dir, env, nil, args)
	return strings.TrimRight(string(out), "\n"), err
}

// RunBytes is Run with stdout returned untouched, for output whose bytes
// matter: a patch needs its final newline, a blob its exact content.
func RunBytes(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return run(ctx, dir, env, nil, args)
}

// RunInput is RunBytes with stdin fed from input (git apply, git notes
// add -F -).
func RunInput(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	return run(ctx, dir, nil, input, args)
}

// RunInputEnv is RunInput with env appended to the process environment,
// as Run takes it.
func RunInputEnv(ctx context.Context, dir string, env []string, input []byte, args ...string) ([]byte, error) {
	return run(ctx, dir, env, input, args)
}

func run(ctx context.Context, dir string, env []string, input []byte, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return stdout.Bytes(), nil
}

// CommitExists reports, as a nil error, that rev names a commit in repo.
// A non-nil error is an *Error whose Detail is git's reason.
func CommitExists(ctx context.Context, repo, rev string) error {
	_, err := run(ctx, repo, nil, nil, []string{"cat-file", "-e", rev + "^{commit}"})
	return err
}

// EmptyTree is the hash of the empty tree in repo, computed rather than
// hardcoded so a sha256 repository gets its own value instead of git's
// well-known sha1 constant.
func EmptyTree(ctx context.Context, repo string) (string, error) {
	out, err := Run(ctx, repo, nil, "hash-object", "-t", "tree", os.DevNull)
	return strings.TrimSpace(out), err
}

// Diff is the binary patch from one tree-ish to another. An empty from
// means the empty tree, so the patch recreates the whole of to.
func Diff(ctx context.Context, repo, from, to string) ([]byte, error) {
	if from == "" {
		empty, err := EmptyTree(ctx, repo)
		if err != nil {
			return nil, err
		}
		from = empty
	}
	args := append([]string{"diff", "--binary"}, PatchFormat...)
	return RunBytes(ctx, repo, nil, append(args, from, to)...)
}

// PatchFormat are the git diff flags for a patch that git apply is later
// given. git diff is porcelain and takes the user's configuration: colour,
// dropped or renamed a/ b/ prefixes, an external diff tool, textconv
// filters. Each gives a patch that git apply refuses or misapplies, so the
// format is spelled out. Callers must not append to it in place.
var PatchFormat = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}

// Apply applies patch (held in memory, never a path: `-C dir` would read a
// relative path from dir rather than from the caller's directory) to the
// work tree at dir.
func Apply(ctx context.Context, dir string, patch []byte) error {
	_, err := RunInput(ctx, dir, patch, "apply", "--binary")
	return err
}

// Short is a commit sha shortened to 12 characters for display.
func Short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
