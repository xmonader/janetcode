// Package gitship is the git and GitHub side of /commit and /pr: which
// of the session's changed files to commit, the commit itself, and
// pushing a branch and opening a pull request with gh. Drafting the
// words is the agent's (it has the model); this package only runs git
// and gh, and refuses what a person would regret: committing a file that
// looks like a credential, sweeping in changes the session did not make,
// or opening a pull request from the base branch onto itself.
package gitship

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/secret"
)

// Timeout bounds one /commit or /pr's git and gh work; a push over a slow
// link is the long pole.
const Timeout = 2 * time.Minute

// maxDiff caps the diff handed to the model for a draft.
const maxDiff = 24000

// CommitPlan is what /commit would do.
type CommitPlan struct {
	Root string
	// Files are the session's changed files git sees as changed, relative
	// to Root: what gets committed.
	Files []string
	// Withheld are changed files that look like credentials, with why.
	Withheld map[string]string
	// Others are files git sees as changed that the session did not
	// change (the person's own edits, or a shell command's); they are
	// left alone and listed so nobody is surprised by what stayed out.
	Others []string
	// Diff is the change being committed, for the drafted message.
	Diff string
}

// PlanCommit works out the commit for the session's changed files
// (absolute paths) in the repository containing dir.
func PlanCommit(ctx context.Context, dir string, changed []string) (CommitPlan, error) {
	root, err := Root(ctx, dir)
	if err != nil {
		return CommitPlan{}, err
	}
	dirty, err := dirtyFiles(ctx, root)
	if err != nil {
		return CommitPlan{}, err
	}
	ours := map[string]bool{}
	realRoot := resolved(root)
	for _, p := range changed {
		rel, err := filepath.Rel(realRoot, resolved(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // outside this repository
		}
		ours[filepath.ToSlash(rel)] = true
	}
	plan := CommitPlan{Root: root, Withheld: map[string]string{}}
	for _, f := range dirty {
		if !ours[f] {
			plan.Others = append(plan.Others, f)
			continue
		}
		if why := looksSecret(root, f); why != "" {
			plan.Withheld[f] = why
			continue
		}
		plan.Files = append(plan.Files, f)
	}
	if len(plan.Files) > 0 {
		plan.Diff = diffOf(ctx, root, plan.Files)
	}
	return plan, nil
}

// resolved is p with symlinks followed in the longest leading part of
// its directory that exists; the file itself is not followed, since a
// symlink git tracks is committed as the link. git reports the top level
// resolved, while a session that started in a symlinked directory names
// its files through the link. A deleted file, or one in a deleted
// directory, keeps its missing tail.
func resolved(p string) string {
	p = filepath.Clean(p)
	dir, tail := filepath.Dir(p), filepath.Base(p)
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, tail)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(p)
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		dir = parent
	}
}

// Root is the top of the repository containing dir.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if gitx.ExitCode(err) > 0 {
			return "", errors.New("not inside a git repository")
		}
		return "", gitx.Wrapf(err, "git")
	}
	return strings.TrimSpace(out), nil
}

// dirtyFiles lists paths with staged, unstaged or untracked changes,
// relative to root. -z keeps names with spaces or quotes intact.
func dirtyFiles(ctx context.Context, root string) ([]string, error) {
	raw, err := gitx.RunBytes(ctx, root, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, gitx.Wrapf(err, "git status")
	}
	var out []string
	recs := bytes.Split(raw, []byte{0})
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 4 {
			continue
		}
		xy, path := string(r[:2]), string(r[3:])
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the source path follows a rename or copy; the new one counts
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

// secretNames are file names that hold credentials far more often than
// not, whatever is in them today.
var secretNames = []string{".env", ".netrc", ".npmrc", ".pypirc", "id_rsa", "id_ecdsa", "id_ed25519", "credentials", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml"}

var secretExts = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"}

// looksSecret says why a file should not be committed, or "".
func looksSecret(root, rel string) string {
	base := strings.ToLower(filepath.Base(rel))
	for _, n := range secretNames {
		if base == n || (n == ".env" && strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") && !strings.HasSuffix(base, ".template")) {
			return "its name is one credentials live in"
		}
	}
	for _, e := range secretExts {
		if strings.HasSuffix(base, e) {
			return "its extension is a key or certificate store's"
		}
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "" // deleted: removing it commits no secret
	}
	if len(data) > 1<<20 {
		data = data[:1<<20]
	}
	if what := secret.Scan(string(data)); what != "" {
		return "it contains what looks like " + what
	}
	return ""
}

// diffOf is the change to files against HEAD, untracked files included,
// capped for a prompt.
func diffOf(ctx context.Context, root string, files []string) string {
	var sb strings.Builder
	args := append([]string{"diff", "HEAD", "--stat", "--patch", "--no-color", "--"}, files...)
	if out, err := gitx.Run(ctx, root, nil, args...); err == nil {
		sb.WriteString(out)
	} else if out, err := gitx.Run(ctx, root, nil, append([]string{"diff", "--cached", "--no-color", "--"}, files...)...); err == nil {
		sb.WriteString(out) // no HEAD yet: the first commit
	}
	untracked, _ := gitx.Run(ctx, root, nil, append([]string{"ls-files", "--others", "--exclude-standard", "--"}, files...)...)
	for _, f := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if f == "" || sb.Len() > maxDiff {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "\nnew file %s:\n%s\n", f, data)
	}
	d := sb.String()
	if len(d) > maxDiff {
		d = d[:maxDiff] + "\n[diff cut here]"
	}
	return d
}

// Commit stages files and commits exactly them: whatever else is staged
// stays staged and out of this commit.
func Commit(ctx context.Context, root string, files []string, msg string) (string, error) {
	if len(files) == 0 {
		return "", errors.New("nothing to commit")
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", errors.New("empty commit message")
	}
	if _, err := gitx.Run(ctx, root, nil, append([]string{"add", "--all", "--"}, files...)...); err != nil {
		return "", gitx.Wrapf(err, "git add")
	}
	if _, err := gitx.RunInput(ctx, root, []byte(msg+"\n"), append([]string{"commit", "--file=-", "--only", "--"}, files...)...); err != nil {
		return "", gitx.Wrapf(err, "git commit")
	}
	sha, err := gitx.Run(ctx, root, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", gitx.Wrapf(err, "git rev-parse")
	}
	return strings.TrimSpace(sha), nil
}

// PRPlan is what /pr would open.
type PRPlan struct {
	Root   string
	Branch string
	Base   string
	Remote string
	// Log is the commits on Branch that Base lacks, oldest first.
	Log string
	// Diff is the branch's change against Base, capped.
	Diff string
	// Existing is the URL of an open pull request for Branch, if there is
	// one: /pr shows it instead of opening a second.
	Existing string
}

// PlanPR gathers what a pull request from the current branch onto base
// needs. An empty base means the remote's default branch.
func PlanPR(ctx context.Context, dir, base string) (PRPlan, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return PRPlan{}, errors.New("/pr opens the pull request with the GitHub CLI, and gh is not on PATH (https://cli.github.com)")
	}
	root, err := Root(ctx, dir)
	if err != nil {
		return PRPlan{}, err
	}
	branch, err := gitx.Run(ctx, root, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return PRPlan{}, errors.New("HEAD is detached; check out a branch for the pull request first")
	}
	p := PRPlan{Root: root, Branch: strings.TrimSpace(branch), Remote: "origin"}
	if r, err := gitx.Run(ctx, root, nil, "config", "--get", "branch."+p.Branch+".remote"); err == nil && strings.TrimSpace(r) != "" && strings.TrimSpace(r) != "." {
		p.Remote = strings.TrimSpace(r)
	}
	if _, err := gitx.Run(ctx, root, nil, "remote", "get-url", p.Remote); err != nil {
		return PRPlan{}, fmt.Errorf("no git remote %q to push %s to", p.Remote, p.Branch)
	}
	p.Base = strings.TrimSpace(base)
	if p.Base == "" {
		p.Base = defaultBranch(ctx, root, p.Remote)
	}
	if p.Base == p.Branch {
		return PRPlan{}, fmt.Errorf("you are on %s, the base branch; create a branch for the change first (git switch -c <name>)", p.Branch)
	}
	baseRef := p.Remote + "/" + p.Base
	if _, err := gitx.Run(ctx, root, nil, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
		baseRef = p.Base
		if _, err := gitx.Run(ctx, root, nil, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
			return PRPlan{}, fmt.Errorf("no base branch %q here or on %s", p.Base, p.Remote)
		}
	}
	log, err := gitx.Run(ctx, root, nil, "log", "--reverse", "--format=%h %s%n%b", baseRef+"..HEAD")
	if err != nil {
		return PRPlan{}, gitx.Wrapf(err, "git log")
	}
	if strings.TrimSpace(log) == "" {
		return PRPlan{}, fmt.Errorf("%s has no commits that %s lacks; commit first (/commit)", p.Branch, p.Base)
	}
	p.Log = strings.TrimSpace(log)
	if d, err := gitx.Run(ctx, root, nil, "diff", "--stat", "--patch", "--no-color", baseRef+"...HEAD"); err == nil {
		if len(d) > maxDiff {
			d = d[:maxDiff] + "\n[diff cut here]"
		}
		p.Diff = d
	}
	if out, err := gh(ctx, root, "", "pr", "view", p.Branch, "--json", "url,state", "--jq", `select(.state=="OPEN") | .url`); err == nil {
		p.Existing = strings.TrimSpace(out)
	}
	return p, nil
}

// defaultBranch is the remote's HEAD branch, else main or master,
// whichever exists.
func defaultBranch(ctx context.Context, root, remote string) string {
	if out, err := gitx.Run(ctx, root, nil, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), remote+"/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := gitx.Run(ctx, root, nil, "rev-parse", "--verify", "--quiet", remote+"/"+b); err == nil {
			return b
		}
	}
	return "main"
}

// OpenPR pushes the branch and opens the pull request, returning its URL.
func OpenPR(ctx context.Context, p PRPlan, title, body string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("empty pull request title")
	}
	// No credential prompt: there is no terminal for one under the TUI,
	// and a push waiting on it would hang /pr until the timeout.
	if _, err := gitx.Run(ctx, p.Root, []string{"GIT_TERMINAL_PROMPT=0"}, "push", "--set-upstream", p.Remote, p.Branch); err != nil {
		return "", gitx.Wrapf(err, "git push")
	}
	out, err := gh(ctx, p.Root, body, "pr", "create", "--base", p.Base, "--head", p.Branch, "--title", title, "--body-file", "-")
	if err != nil {
		return "", err
	}
	// gh prints the URL as its last line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// gh runs the GitHub CLI with stdin as its input.
func gh(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("gh %s: %s", args[0]+" "+args[1], msg)
	}
	return stdout.String(), nil
}
