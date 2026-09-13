package proof

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Proof notes are local only until pushed: `git push` and `git fetch`
// never move refs/notes/* on their own, so a note attached in one clone
// stays invisible to everyone else until someone moves it across
// explicitly. Push, Fetch and ConfigureFetch are that explicit step,
// shared by /proof push|fetch and `janetcode --proof push|fetch` (see
// ParseArgs).

// Push pushes this repository's proof notes to remote (default
// DefaultRemote). It is an explicit user action (`/proof push`), so
// unlike an automatic write it runs without asking for confirmation.
func Push(ctx context.Context, repoDir, remote string) error {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	_, err := runGit(ctx, repoDir, "push", remote, NotesRef)
	return err
}

// Fetch fetches remote's proof notes into this repository's own notes
// ref, so notes another session pushed become visible here.
func Fetch(ctx context.Context, repoDir, remote string) error {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	_, err := runGit(ctx, repoDir, "fetch", remote, NotesRef+":"+NotesRef)
	return err
}

// noteFetchRefspec is what ConfigureFetch adds to remote.<remote>.fetch,
// so that an ordinary `git fetch` also brings proof notes without a
// separate `/proof fetch` every time.
func noteFetchRefspec() string { return "+" + NotesRef + ":" + NotesRef }

// ConfigureFetch adds the proof notes refspec to remote's fetch config if
// it is not already there.
func ConfigureFetch(ctx context.Context, repoDir, remote string) error {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	key := "remote." + remote + ".fetch"
	refspec := noteFetchRefspec()
	cmd := exec.CommandContext(ctx, "git", "config", "--get-all", key)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		// Exit 1 means the key has no values yet -- not an error here.
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return fmt.Errorf("git config --get-all %s: %w", key, err)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == refspec {
			return nil
		}
	}
	if _, err := runGit(ctx, repoDir, "config", "--add", key, refspec); err != nil {
		return fmt.Errorf("git config --add %s %s: %w", key, refspec, err)
	}
	return nil
}
