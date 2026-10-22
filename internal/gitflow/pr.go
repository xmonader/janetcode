package gitflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/secret"
)

// GH is the GitHub CLI's name or path. Tests point it at a fake.
var GH = "gh"

// maxFetched caps what /pr-comments and /issue put into the conversation.
const maxFetched = 32 * 1024

// PRPlan is what /pr would do: push Branch to Remote and open a pull
// request from it against Base.
type PRPlan struct {
	Root, Branch, Base, Remote, RemoteURL string
	// Commits are the branch's commits not on Base, oneline, newest first.
	Commits []string
	// Log is their full messages, for the drafting prompt.
	Log string
	// Stat is the diffstat against Base.
	Stat string
	// GH is true when gh is installed and logged in: the pull request is
	// opened with it. Otherwise the branch is pushed and a compare URL
	// printed.
	GH bool
}

// PlanPR reads the branch, its base and its remote.
func PlanPR(ctx context.Context, dir string) (PRPlan, error) {
	root, err := gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return PRPlan{}, fmt.Errorf("not a git repository")
	}
	p := PRPlan{Root: strings.TrimSpace(root)}
	branch, err := gitx.Run(ctx, p.Root, nil, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return PRPlan{}, fmt.Errorf("HEAD is detached: check out a branch first")
	}
	p.Branch = strings.TrimSpace(branch)
	p.Remote = remoteFor(ctx, p.Root, p.Branch)
	if p.Remote == "" {
		return PRPlan{}, fmt.Errorf("this repository has no remote to push to")
	}
	url, _ := gitx.Run(ctx, p.Root, nil, "remote", "get-url", p.Remote)
	p.RemoteURL = strings.TrimSpace(url)
	p.Base = baseBranch(ctx, p.Root, p.Remote)
	if p.Base == "" {
		return PRPlan{}, fmt.Errorf("cannot tell which branch to open the pull request against (no %s/HEAD, main or master)", p.Remote)
	}
	if p.Base == p.Branch {
		return PRPlan{}, fmt.Errorf("you are on %s, the base branch: create a branch for the change first", p.Base)
	}
	baseRef := p.Remote + "/" + p.Base
	if gitx.CommitExists(ctx, p.Root, baseRef) != nil {
		baseRef = p.Base
	}
	commits, _ := gitx.Run(ctx, p.Root, nil, "log", "--oneline", baseRef+"..HEAD")
	p.Commits = lines(commits)
	if len(p.Commits) == 0 {
		return PRPlan{}, fmt.Errorf("%s has no commits that %s lacks: nothing to open a pull request for", p.Branch, p.Base)
	}
	p.Log, _ = gitx.Run(ctx, p.Root, nil, "log", "--format=%s%n%n%b", baseRef+"..HEAD")
	p.Stat, _ = gitx.Run(ctx, p.Root, nil, "diff", "--stat", baseRef+"...HEAD")
	p.GH = GHReady(ctx)
	return p, nil
}

// remoteFor is the branch's configured remote, else origin, else the only
// remote there is.
func remoteFor(ctx context.Context, root, branch string) string {
	if r, err := gitx.Run(ctx, root, nil, "config", "branch."+branch+".remote"); err == nil && strings.TrimSpace(r) != "" && strings.TrimSpace(r) != "." {
		return strings.TrimSpace(r)
	}
	out, _ := gitx.Run(ctx, root, nil, "remote")
	remotes := lines(out)
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) == 1 {
		return remotes[0]
	}
	return ""
}

// baseBranch is the remote's default branch, else main or master.
func baseBranch(ctx context.Context, root, remote string) string {
	if ref, err := gitx.Run(ctx, root, nil, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(ref), remote+"/")
	}
	for _, b := range []string{"main", "master"} {
		if gitx.CommitExists(ctx, root, remote+"/"+b) == nil || gitx.CommitExists(ctx, root, "refs/heads/"+b) == nil {
			return b
		}
	}
	return ""
}

// GHReady reports that gh is installed and logged in.
func GHReady(ctx context.Context) bool {
	if _, err := exec.LookPath(GH); err != nil {
		return false
	}
	return exec.CommandContext(ctx, GH, "auth", "status").Run() == nil
}

// Preview says what /pr create will do.
func (p PRPlan) Preview() string {
	var b strings.Builder
	fmt.Fprintf(&b, "push %s to %s (%s) and open a pull request against %s", p.Branch, p.Remote, p.RemoteURL, p.Base)
	if !p.GH {
		b.WriteString(" · gh is not installed or not logged in, so the branch is pushed and the compare URL printed")
	}
	fmt.Fprintf(&b, "\n%d commits:\n", len(p.Commits))
	for _, c := range p.Commits {
		b.WriteString("  " + c + "\n")
	}
	if p.Stat != "" {
		b.WriteString(p.Stat)
	}
	return strings.TrimRight(b.String(), "\n")
}

// FallbackDraft is a title and body built from the commits alone: the
// subject of a single commit, else the branch name, and the subjects as a
// list. It is the draft when the model gives none.
func (p PRPlan) FallbackDraft() (title, body string) {
	subject := func(oneline string) string {
		_, s, _ := strings.Cut(oneline, " ")
		return s
	}
	if len(p.Commits) == 1 {
		return subject(p.Commits[0]), ""
	}
	title = strings.NewReplacer("-", " ", "_", " ", "/", ": ").Replace(p.Branch)
	var b strings.Builder
	for i := len(p.Commits) - 1; i >= 0; i-- {
		b.WriteString("- " + subject(p.Commits[i]) + "\n")
	}
	return title, strings.TrimRight(b.String(), "\n")
}

// PRPrompt asks the model for a pull request title and body.
func PRPrompt(p PRPlan) string {
	var b strings.Builder
	b.WriteString("Write a pull request title and description for the commits below. You have no tools for this reply: everything you need is here. Reply in exactly this shape, nothing before or after:\n")
	b.WriteString("TITLE: <one line under 72 characters>\n\n<body in Markdown: what changed and why, and how it was tested only if this conversation or the commits show it (never invent testing); short>\n\n")
	fmt.Fprintf(&b, "Branch %s into %s.\n\nCommits:\n", p.Branch, p.Base)
	log, _ := secret.Redact(p.Log)
	b.WriteString(log + "\n\nDiffstat:\n" + p.Stat)
	return b.String()
}

// ParseDraft splits a PRPrompt reply into title and body. A reply that
// ignored the shape is taken as a title line followed by the body.
func ParseDraft(reply string) (title, body string) {
	reply = CleanMessage(reply)
	first, rest, _ := strings.Cut(reply, "\n")
	first = strings.TrimSpace(first)
	for _, label := range []string{"TITLE:", "Title:", "title:", "# "} {
		first = strings.TrimSpace(strings.TrimPrefix(first, label))
	}
	return first, strings.TrimSpace(rest)
}

// PRResult is what /pr create did.
type PRResult struct {
	// URL is the pull request's, or the compare page's when gh could not
	// open one.
	URL string
	// Opened is true when the pull request exists now.
	Opened bool
}

// CreatePR pushes the branch and opens the pull request.
func CreatePR(ctx context.Context, dir, title, body string) (PRResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return PRResult{}, fmt.Errorf("a pull request needs a title")
	}
	p, err := PlanPR(ctx, dir)
	if err != nil {
		return PRResult{}, err
	}
	if _, err := gitx.Run(ctx, p.Root, nil, "push", "-u", p.Remote, p.Branch); err != nil {
		return PRResult{}, gitx.Wrapf(err, "push")
	}
	if !p.GH {
		url := CompareURL(p.RemoteURL, p.Base, p.Branch)
		if url == "" {
			return PRResult{}, fmt.Errorf("pushed %s, but gh is not available and %s is not a GitHub remote: open the pull request by hand", p.Branch, p.RemoteURL)
		}
		return PRResult{URL: url}, nil
	}
	out, err := runGH(ctx, p.Root, []byte(body), "pr", "create", "--base", p.Base, "--head", p.Branch, "--title", title, "--body-file", "-")
	if err != nil {
		return PRResult{}, fmt.Errorf("pushed %s, but gh pr create failed: %w", p.Branch, err)
	}
	return PRResult{URL: lastURL(out), Opened: true}, nil
}

var urlRe = regexp.MustCompile(`https?://\S+`)

func lastURL(s string) string {
	all := urlRe.FindAllString(s, -1)
	if len(all) == 0 {
		return strings.TrimSpace(s)
	}
	return all[len(all)-1]
}

var ghRemoteRe = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?|ssh://git@|git@)github\.com[:/]([^/]+)/(.+?)(?:\.git)?/?$`)

// CompareURL is GitHub's page for opening a pull request from branch into
// base, or "" when remote is not a GitHub URL.
func CompareURL(remote, base, branch string) string {
	m := ghRemoteRe.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s?expand=1", m[1], m[2], base, branch)
}

func runGH(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, GH, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if _, lerr := exec.LookPath(GH); lerr != nil {
			return "", fmt.Errorf("gh is not installed: https://cli.github.com")
		}
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", err
	}
	return out.String(), nil
}

type ghComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body  string `json:"body"`
	State string `json:"state"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
}

func (c ghComment) who() string {
	if c.Author.Login != "" {
		return c.Author.Login
	}
	if c.User.Login != "" {
		return c.User.Login
	}
	return "someone"
}

// PRComments is the pull request's description, reviews and comments,
// inline ones with their file and line, as text for the model. n empty
// means the current branch's pull request.
func PRComments(ctx context.Context, dir, n string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(n), "-") {
		return "", fmt.Errorf("usage: /pr-comments [number|url|branch]")
	}
	args := []string{"pr", "view"}
	if n != "" {
		args = append(args, n)
	}
	args = append(args, "--json", "number,title,url,state,body,comments,reviews")
	out, err := runGH(ctx, dir, nil, args...)
	if err != nil {
		return "", fmt.Errorf("pr-comments: %w", err)
	}
	var pr struct {
		Number   int         `json:"number"`
		Title    string      `json:"title"`
		URL      string      `json:"url"`
		State    string      `json:"state"`
		Body     string      `json:"body"`
		Comments []ghComment `json:"comments"`
		Reviews  []ghComment `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return "", fmt.Errorf("pr-comments: gh answered something that is not a pull request: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%d: %s (%s)\n%s\n", pr.Number, pr.Title, strings.ToLower(pr.State), pr.URL)
	if body := strings.TrimSpace(pr.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	for _, r := range pr.Reviews {
		if strings.TrimSpace(r.Body) == "" && r.State == "COMMENTED" {
			continue
		}
		fmt.Fprintf(&b, "\nReview by %s (%s):\n%s\n", r.who(), strings.ToLower(strings.ReplaceAll(r.State, "_", " ")), strings.TrimSpace(r.Body))
	}
	inline, err := runGH(ctx, dir, nil, "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", pr.Number))
	if err == nil {
		var cs []ghComment
		if json.Unmarshal([]byte(inline), &cs) == nil {
			for _, c := range cs {
				fmt.Fprintf(&b, "\n%s:%d, %s:\n%s\n", c.Path, c.Line, c.who(), strings.TrimSpace(c.Body))
			}
		}
	}
	for _, c := range pr.Comments {
		fmt.Fprintf(&b, "\nComment by %s:\n%s\n", c.who(), strings.TrimSpace(c.Body))
	}
	return capText(b.String()), nil
}

// Issue is an issue's title, state, labels, body and comments as text.
func Issue(ctx context.Context, dir, n string) (string, error) {
	n = strings.TrimPrefix(strings.TrimSpace(n), "#")
	if n == "" || strings.HasPrefix(n, "-") {
		return "", fmt.Errorf("usage: /issue <number|url>")
	}
	out, err := runGH(ctx, dir, nil, "issue", "view", n, "--json", "number,title,url,state,body,comments,labels")
	if err != nil {
		return "", fmt.Errorf("issue: %w", err)
	}
	var is struct {
		Number   int         `json:"number"`
		Title    string      `json:"title"`
		URL      string      `json:"url"`
		State    string      `json:"state"`
		Body     string      `json:"body"`
		Comments []ghComment `json:"comments"`
		Labels   []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &is); err != nil {
		return "", fmt.Errorf("issue: gh answered something that is not an issue: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Issue #%d: %s (%s)\n%s\n", is.Number, is.Title, strings.ToLower(is.State), is.URL)
	if len(is.Labels) > 0 {
		var ls []string
		for _, l := range is.Labels {
			ls = append(ls, l.Name)
		}
		b.WriteString("labels: " + strings.Join(ls, ", ") + "\n")
	}
	if body := strings.TrimSpace(is.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	for _, c := range is.Comments {
		fmt.Fprintf(&b, "\nComment by %s:\n%s\n", c.who(), strings.TrimSpace(c.Body))
	}
	return capText(b.String()), nil
}

func capText(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) > maxFetched {
		s = s[:maxFetched] + "\n… (truncated)"
	}
	return s
}

// SplitCreate reads "/pr create" arguments: the first line is the title,
// the rest the body.
func SplitCreate(arg string) (title, body string) {
	title, body, _ = strings.Cut(strings.TrimSpace(arg), "\n")
	return strings.TrimSpace(title), strings.TrimSpace(body)
}
