// Package gitflow is the git and GitHub side of /commit, /pr, /pr-comments
// and /issue, shared by the terminal and the web UI: what a commit would
// hold, the prompt that asks the model to draft its message, the commit
// itself, and the push and pull request that follow. It never calls a
// model; the front ends pass its prompts to the session's own, so a
// draft is written with the conversation that produced the change.
package gitflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/secret"
)

// maxDiff caps the diff a drafting prompt carries, as /review caps its own.
const maxDiff = 32 * 1024

// Change is what a commit made now would hold.
type Change struct {
	// Root is the repository's top-level directory.
	Root string
	// Staged is true when the index already holds changes: then only they
	// are committed, as `git commit` would. Otherwise every change in the
	// work tree is, untracked files included, minus Held.
	Staged bool
	// Files are the paths the commit would carry, with git's status letter.
	Files []string
	// Held are untracked files left out because their names say they hold
	// credentials (.env, *.pem, id_rsa…); git add them by hand if meant.
	Held []string
	// Diff is the patch, capped at maxDiff.
	Diff string
	// Secret names a credential found in the patch's added lines, empty
	// when none was.
	Secret string
}

// Empty reports that there is nothing to commit.
func (c Change) Empty() bool { return len(c.Files) == 0 }

// Pending reads what a commit in dir would hold now.
func Pending(ctx context.Context, dir string) (Change, error) {
	root, err := gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return Change{}, fmt.Errorf("not a git repository")
	}
	c := Change{Root: strings.TrimSpace(root)}
	staged, _ := gitx.Run(ctx, c.Root, nil, "diff", "--cached", "--name-status")
	if strings.TrimSpace(staged) != "" {
		c.Staged = true
		c.Files = lines(staged)
		c.Diff, _ = gitx.Run(ctx, c.Root, nil, "diff", "--cached")
	} else {
		tracked, _ := gitx.Run(ctx, c.Root, nil, "diff", "HEAD", "--name-status")
		if strings.TrimSpace(tracked) == "" {
			// A repository with no commit yet has no HEAD to diff against.
			tracked, _ = gitx.Run(ctx, c.Root, nil, "diff", "--name-status")
		}
		c.Files = lines(tracked)
		c.Diff, _ = gitx.Run(ctx, c.Root, nil, "diff", "HEAD")
		if c.Diff == "" {
			c.Diff, _ = gitx.Run(ctx, c.Root, nil, "diff")
		}
		// -z: without it git quotes a name it finds unusual (non-ASCII,
		// a quote, a tab), and `git add --` of the quoted spelling
		// matches no file, failing the whole commit.
		untracked, _ := gitx.Run(ctx, c.Root, nil, "ls-files", "-z", "--others", "--exclude-standard")
		for _, f := range strings.Split(untracked, "\x00") {
			if f == "" {
				continue
			}
			if credentialName(f) {
				c.Held = append(c.Held, f)
				continue
			}
			c.Files = append(c.Files, "A\t"+f)
			c.Diff += newFilePatch(c.Root, f)
		}
	}
	c.Secret = secret.Scan(addedLines(c.Diff))
	if len(c.Diff) > maxDiff {
		c.Diff = c.Diff[:maxDiff] + "\n… (diff truncated)"
	}
	return c, nil
}

// newFilePatch shows an untracked file the way git diff would once it is
// added, so the draft and the credential scan both see its content. A
// binary or large file is named only.
func newFilePatch(root, name string) string {
	head := "\ndiff --git a/" + name + " b/" + name + "\nnew file\n--- /dev/null\n+++ b/" + name + "\n"
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || len(data) > 64*1024 || bytes.IndexByte(data, 0) >= 0 {
		return head + "(binary or large file)\n"
	}
	var b strings.Builder
	b.WriteString(head)
	for _, l := range strings.SplitAfter(string(data), "\n") {
		if l != "" {
			b.WriteString("+" + l)
		}
	}
	return b.String()
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// addedLines is the text a patch adds, so a secret it removes is not
// reported as one it commits.
func addedLines(diff string) string {
	var b strings.Builder
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			b.WriteString(l[1:])
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// credentialName is an untracked file whose name says it holds a key, so
// staging "everything" does not sweep it into history.
func credentialName(p string) bool {
	base := strings.ToLower(path.Base(p))
	switch {
	case base == ".env", strings.HasPrefix(base, ".env.") && base != ".env.example" && base != ".env.sample":
		return true
	case strings.HasPrefix(base, "id_rsa"), strings.HasPrefix(base, "id_ed25519"), strings.HasPrefix(base, "id_ecdsa"):
		return true
	case base == ".netrc", base == ".npmrc", base == ".pypirc", base == "credentials", base == "credentials.json":
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// RecentSubjects are the subjects of the last n commits, the style a
// drafted message should follow.
func RecentSubjects(ctx context.Context, dir string, n int) []string {
	out, err := gitx.Run(ctx, dir, nil, "log", fmt.Sprintf("-%d", n), "--format=%s")
	if err != nil {
		return nil
	}
	return lines(out)
}

// CommitPrompt asks the model for a commit message for c, in the style of
// the repository's recent subjects.
func CommitPrompt(c Change, subjects []string) string {
	var b strings.Builder
	b.WriteString("Write a git commit message for the change below. You have no tools for this reply: everything you need is here. Reply with the message only: no preamble, no code fence, no quotes.\n")
	b.WriteString("First line: a subject under 72 characters. If the change needs explaining, a blank line and a short body saying why, wrapped at 72.\n")
	if len(subjects) > 0 {
		b.WriteString("Follow the style of this repository's recent subjects:\n")
		for _, s := range subjects {
			b.WriteString("  " + s + "\n")
		}
	}
	b.WriteString("\nFiles:\n")
	for _, f := range c.Files {
		b.WriteString("  " + f + "\n")
	}
	// The prompt leaves the machine the way a tool result does, and gets
	// the same redaction a tool result would.
	diff, _ := secret.Redact(c.Diff)
	b.WriteString("\n" + diff)
	return b.String()
}

// CleanMessage strips what a model wraps a message in despite being told
// not to: a code fence, surrounding quotes, a "Commit message:" label.
func CleanMessage(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 && !strings.Contains(s[:i], " ") {
			s = s[i+1:] // the fence's language tag
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	s = strings.TrimSpace(s)
	for _, label := range []string{"commit message:", "message:", "subject:"} {
		if strings.HasPrefix(strings.ToLower(s), label) {
			s = strings.TrimSpace(s[len(label):])
		}
	}
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// Commit commits the change Pending describes with msg and returns the
// new commit's hash. Staged changes are committed alone; with nothing
// staged, every change is staged first, except files whose names say they
// hold credentials.
func Commit(ctx context.Context, dir, msg string) (string, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", fmt.Errorf("empty commit message")
	}
	c, err := Pending(ctx, dir)
	if err != nil {
		return "", err
	}
	if c.Empty() {
		return "", fmt.Errorf("nothing to commit")
	}
	if !c.Staged {
		if _, err := gitx.Run(ctx, c.Root, nil, "add", "-u"); err != nil {
			return "", gitx.Wrapf(err, "stage")
		}
		var add []string
		for _, f := range c.Files {
			if st, p, ok := strings.Cut(f, "\t"); ok && st == "A" {
				add = append(add, p)
			}
		}
		if len(add) > 0 {
			if _, err := gitx.Run(ctx, c.Root, nil, append([]string{"add", "--"}, add...)...); err != nil {
				return "", gitx.Wrapf(err, "stage")
			}
		}
	}
	if _, err := gitx.RunInput(ctx, c.Root, []byte(msg+"\n"), "commit", "-q", "-F", "-"); err != nil {
		return "", gitx.Wrapf(err, "commit")
	}
	sha, err := gitx.Run(ctx, c.Root, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// Preview is what the user reads before accepting a drafted message.
func (c Change) Preview() string {
	var b strings.Builder
	what := "every change in the work tree"
	if c.Staged {
		what = "the staged changes only"
	}
	fmt.Fprintf(&b, "commit %s (%d files):\n", what, len(c.Files))
	for _, f := range c.Files {
		b.WriteString("  " + strings.Replace(f, "\t", " ", 1) + "\n")
	}
	if len(c.Held) > 0 {
		b.WriteString("left out, names look like credentials (git add them yourself if meant): " + strings.Join(c.Held, ", ") + "\n")
	}
	if c.Secret != "" {
		b.WriteString("warning: the change adds what looks like a credential (" + c.Secret + ")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
