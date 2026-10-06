package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/gitship"
	"github.com/xmonader/janetcode/internal/llm"
)

// /commit and /pr: the session's work, committed and proposed, in words
// the model drafts from the diff and from the intents it recorded for
// each change. The git side, and everything it refuses, is gitship's.

const commitSystem = `Write a git commit message for the change below. First line: an imperative summary under 72 characters, in the conventional "type(scope): subject" form when the change fits one (feat, fix, refactor, docs, test, chore). Then, only if the change needs explaining, a blank line and a short body saying why, wrapped at 72 columns. Reply with the message alone: no code fences, no quotes, no preamble.`

const prSystem = `Write a GitHub pull request for the commits and diff below. Reply in exactly this shape and nothing else:
TITLE: <one line under 72 characters>
BODY:
<markdown: what changed and why, then how it was tested if the commits or diff show it. Short; no headings for a small change.>`

// CommitCommand runs /commit [message]: commit the files this session
// changed, with message or a drafted one.
func (a *Agent) CommitCommand(ctx context.Context, cwd, message string) (cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, gitship.Timeout)
	defer cancel()
	plan, err := gitship.PlanCommit(ctx, cwd, a.changedPaths())
	if err != nil {
		return cmdresult.Result{Error: "commit: " + err.Error()}, nil
	}
	var res cmdresult.Result
	withheld := withheldTips(plan.Withheld)
	if len(plan.Files) == 0 {
		res.Error = "nothing to commit: this session has no uncommitted changes"
		if len(plan.Withheld) > 0 {
			res.Error = "nothing to commit: the only changed files look like credentials"
		}
		res.Tips = append(withheld, othersTip(plan.Others)...)
		return res, nil
	}
	message = strings.TrimSpace(message)
	if message == "" {
		if message, err = a.draftCommit(ctx, plan.Diff); err != nil {
			return cmdresult.Result{Error: "commit: drafting the message failed: " + err.Error() + " · /commit <message> commits with your own"}, nil
		}
	}
	sha, err := gitship.Commit(ctx, plan.Root, plan.Files, message)
	if err != nil {
		return cmdresult.Result{Error: "commit: " + err.Error()}, nil
	}
	subject, _, _ := strings.Cut(message, "\n")
	res.Notes = []string{fmt.Sprintf("committed %s · %s · %s", short(sha), subject, plural(len(plan.Files), "file"))}
	if !a.Off["proof"] && a.Settings().ProofNotes {
		if sum, perr := a.AttachProof(ctx, plan.Root, sha); perr == nil && sum != "" {
			res.Notes = append(res.Notes, "proof attached · /proof shows it")
		}
	}
	res.Tips = append(withheld, othersTip(plan.Others)...)
	return res, nil
}

// PRCommand runs /pr [base]: push the branch and open a pull request.
func (a *Agent) PRCommand(ctx context.Context, cwd, base string) (cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, gitship.Timeout)
	defer cancel()
	plan, err := gitship.PlanPR(ctx, cwd, base)
	if err != nil {
		return cmdresult.Result{Error: "pr: " + err.Error()}, nil
	}
	if plan.Existing != "" {
		return cmdresult.Result{Notes: []string{"a pull request for " + plan.Branch + " is already open: " + plan.Existing}, Tips: []string{"push new commits with git push; the pull request picks them up"}}, nil
	}
	title, body, err := a.draftPR(ctx, plan)
	if err != nil {
		return cmdresult.Result{Error: "pr: drafting the description failed: " + err.Error()}, nil
	}
	url, err := gitship.OpenPR(ctx, plan, title, body)
	if err != nil {
		return cmdresult.Result{Error: "pr: " + err.Error()}, nil
	}
	return cmdresult.Result{Notes: []string{fmt.Sprintf("opened %s → %s: %s", plan.Branch, plan.Base, url)}, Sheet: "# " + title + "\n\n" + body, SheetTitle: "pull request", Plain: true}, nil
}

// changedPaths is every file this session's tools changed and did not
// undo, once each.
func (a *Agent) changedPaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range a.Changes() {
		if c.Undone || seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c.Path)
	}
	return out
}

// intents is what the session said each change was for, most recent
// last, without repeats: the why a diff cannot show.
func (a *Agent) intents() string {
	seen := map[string]bool{}
	var lines []string
	for _, c := range a.Changes() {
		if c.Undone || c.Intent == "" || seen[c.Intent] {
			continue
		}
		seen[c.Intent] = true
		lines = append(lines, "- "+c.Intent)
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

func (a *Agent) draftCommit(ctx context.Context, diff string) (string, error) {
	var sb strings.Builder
	if in := a.intents(); in != "" {
		sb.WriteString("What each change was for:\n" + in + "\n\n")
	}
	sb.WriteString("Diff:\n" + diff)
	msg, err := a.draft(ctx, commitSystem, sb.String(), 400)
	if err != nil {
		return "", err
	}
	msg = strings.TrimSpace(strings.Trim(strings.TrimSpace(msg), "`"))
	if msg == "" {
		return "", errors.New("the model returned an empty message")
	}
	return msg, nil
}

func (a *Agent) draftPR(ctx context.Context, p gitship.PRPlan) (string, string, error) {
	prompt := fmt.Sprintf("Branch %s into %s.\n\nCommits:\n%s\n\nDiff:\n%s", p.Branch, p.Base, p.Log, p.Diff)
	out, err := a.draft(ctx, prSystem, prompt, 1200)
	if err != nil {
		return "", "", err
	}
	title, body := parsePR(out)
	if title == "" {
		// The model ignored the shape: the first commit's subject is a
		// title nobody will be surprised by.
		first, _, _ := strings.Cut(p.Log, "\n")
		if _, subj, ok := strings.Cut(first, " "); ok {
			title = subj
		}
		body = strings.TrimSpace(out)
	}
	if title == "" {
		return "", "", errors.New("the model returned no title")
	}
	return title, body, nil
}

// parsePR reads the TITLE:/BODY: reply.
func parsePR(s string) (title, body string) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, "TITLE:")
	if !ok {
		return "", ""
	}
	line, after, _ := strings.Cut(rest, "\n")
	title = strings.TrimSpace(line)
	after = strings.TrimSpace(after)
	after = strings.TrimPrefix(after, "BODY:")
	return title, strings.TrimSpace(after)
}

// draft is one side call on the session's model, outside the
// conversation: no system prompt of the session's, no tools.
func (a *Agent) draft(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	s := a.Settings()
	if s.Provider == nil {
		return "", errors.New("no provider")
	}
	resp, err := s.Provider.Stream(ctx, llm.Request{
		Model:     s.Model,
		System:    system,
		Messages:  []llm.Message{llm.TextMessage(llm.RoleUser, prompt)},
		MaxTokens: maxTokens,
		Effort:    "low",
	}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("empty reply")
	}
	a.addUsage(s.Model, resp.Usage)
	return resp.Message.Text(), nil
}

func withheldTips(w map[string]string) []string {
	names := make([]string, 0, len(w))
	for n := range w {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		out = append(out, fmt.Sprintf("left out %s: %s; commit it yourself if it is meant to be shared", n, w[n]))
	}
	return out
}

func othersTip(others []string) []string {
	switch len(others) {
	case 0:
		return nil
	case 1:
		return []string{"left alone: " + others[0] + " (changed, but not by this session's file tools)"}
	}
	return []string{fmt.Sprintf("left alone: %s (changed, but not by this session's file tools)", plural(len(others), "other file"))}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
