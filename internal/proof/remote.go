package proof

import (
	"bytes"
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
//
// Before pushing, it checks how many of the commits the local notes ref
// annotates are not yet reachable from remote (as last fetched into
// refs/remotes/<remote>/*): a note on a commit the remote has never seen
// describes work nobody there can look at yet. That is never a reason to
// skip the push — the notes ref is pushed either way, since a note is
// harmless to have out there and other commits' notes may well already be
// on the remote — but the returned warning says how many aren't, so the
// caller can print it. warning is "" when every noted commit is already
// on the remote, or when the check itself could not be done (an empty or
// unreachable remote should not turn into a false alarm, nor block the
// push that was actually asked for).
func Push(ctx context.Context, repoDir, remote string) (warning string, err error) {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	warning = notPushedWarning(ctx, repoDir, remote)
	if _, err := runGit(ctx, repoDir, "push", remote, NotesRef); err != nil {
		return "", err
	}
	return warning, nil
}

// notPushedWarning is Push's best-effort pre-flight: how many noted
// commits remote does not have yet, as the message a front-end prints, or
// "" when there is nothing to say.
func notPushedWarning(ctx context.Context, repoDir, remote string) string {
	shas, err := notedCommits(ctx, repoDir)
	if err != nil || len(shas) == 0 {
		return ""
	}
	n := 0
	for _, sha := range shas {
		on, err := commitOnRemote(ctx, repoDir, remote, sha)
		if err != nil {
			// The check itself is unreliable here (a malformed remote
			// name, a git that cannot resolve refs/remotes/<remote>/*);
			// staying silent is safer than reporting a count that may be
			// wrong in either direction.
			return ""
		}
		if !on {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d noted commit(s) are not on %s yet; push your branch too", n, remote)
}

// notedCommits lists the commit shas this repository's proof notes
// annotate, from `git notes list`, which prints one "<note-blob-sha>
// <annotated-object-sha>" line per note.
func notedCommits(ctx context.Context, repoDir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "notes", "--ref="+NotesRef, "list")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// No notes ref yet is not a real error: there is simply nothing to
		// warn about.
		if strings.Contains(stderr.String(), "No note") || strings.Contains(stderr.String(), "does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("git notes list: %s", firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return nil, nil
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		shas = append(shas, fields[1])
	}
	return shas, nil
}

// commitOnRemote reports whether commit is reachable from any of remote's
// own refs, as last fetched into refs/remotes/<remote>/*: `git rev-list
// --remotes=<remote>` treats those refs as its own starting points, so
// excluding them from a walk that starts at commit ("--not --remotes=…")
// leaves nothing when commit already sits in the remote's history.
func commitOnRemote(ctx context.Context, repoDir, remote, commit string) (bool, error) {
	out, err := runGit(ctx, repoDir, "rev-list", "-n1", commit, "--not", "--remotes="+remote)
	if err != nil {
		return false, err
	}
	return out == "", nil
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
	// /proof fetch is, along with Attach, one of the two places a
	// repository first becomes proof-aware; make it survive amend/rebase
	// from here too rather than only once a note is attached locally.
	if err := EnsureRewriteConfig(ctx, repoDir); err != nil {
		return err
	}
	return addConfigValueIfAbsent(ctx, repoDir, "remote."+remote+".fetch", noteFetchRefspec())
}

// EnsureRewriteConfig is what makes a proof note survive `git commit
// --amend` and `git rebase`, which otherwise silently drop it: git only
// copies a note onto a rewritten commit when notes.rewriteRef names this
// notes ref and notes.rewrite.<command> allows it for the command doing
// the rewriting. Attach calls this itself on every attach (cheap once the
// config is already there, since both helpers below no-op when it is),
// so a session need take no extra step for its own notes to survive an
// amend or rebase; ConfigureFetch calls it too, since /proof fetch is the
// other place a repository first becomes proof-aware.
//
// Existing config is only added to, never overwritten: an explicit
// notes.rewrite.amend=false the user (or another tool) set on purpose is
// left exactly as they set it — only a key nobody has touched gets
// janetcode's own default of true.
func EnsureRewriteConfig(ctx context.Context, repoDir string) error {
	if err := addConfigValueIfAbsent(ctx, repoDir, "notes.rewriteRef", NotesRef); err != nil {
		return err
	}
	for _, key := range []string{"notes.rewrite.amend", "notes.rewrite.rebase"} {
		if err := setConfigIfUnset(ctx, repoDir, key, "true"); err != nil {
			return err
		}
	}
	return nil
}

// addConfigValueIfAbsent adds value to the multi-valued git config key
// unless it is already one of the values key holds.
func addConfigValueIfAbsent(ctx context.Context, repoDir, key, value string) error {
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
		if strings.TrimSpace(line) == value {
			return nil
		}
	}
	if _, err := runGit(ctx, repoDir, "config", "--add", key, value); err != nil {
		return fmt.Errorf("git config --add %s %s: %w", key, value, err)
	}
	return nil
}

// setConfigIfUnset sets the single-valued git config key to value unless
// it already has any value at all.
func setConfigIfUnset(ctx context.Context, repoDir, key, value string) error {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	cmd.Dir = repoDir
	if err := cmd.Run(); err == nil {
		return nil // already set, to whatever value; leave it alone
	} else {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return fmt.Errorf("git config --get %s: %w", key, err)
		}
	}
	if _, err := runGit(ctx, repoDir, "config", key, value); err != nil {
		return fmt.Errorf("git config %s %s: %w", key, value, err)
	}
	return nil
}
