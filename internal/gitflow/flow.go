package gitflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// Asker drafts text with the session's model and conversation
// (agent.Agent.AskOnce). A nil Asker leaves the draft for the user.
type Asker interface {
	AskOnce(ctx context.Context, question string) (string, error)
}

// AttachFunc attaches the session's proof note to a new commit, as
// agent.Agent.AttachProof does; nil attaches none.
type AttachFunc func(ctx context.Context, sha string) bool

// Result is what a front end shows after one of these commands.
type Result struct {
	// Info is shown in the transcript.
	Info string
	// Compose, when set, goes in the input box for the user to edit and
	// send: a drafted "/commit -m …" or "/pr create …".
	Compose string
	// Shared, when set, goes to the model as context for the next turn.
	Shared string
	// Note is the one-line status.
	Note string
	Err  error
}

// draftText is the part of a drafting reply fit for the input box, false
// when there is none. A model with a session's tools in its history will
// sometimes answer a drafting question by calling one — written as text,
// since the request carries no tools — and markup like that is not a
// commit message: a DeepSeek reply of "<｜DSML｜ invoke name="bash">…"
// went into the input box as a pull request title.
func draftText(reply string) (string, bool) {
	if _, rest, ok := llm.RecoverToolCalls(reply); ok {
		reply = rest
	}
	for _, mark := range []string{"<｜", "<|", "<invoke", "<tool_call", "<function", "</parameter"} {
		if strings.Contains(reply, mark) {
			return "", false
		}
	}
	reply = CleanMessage(reply)
	return reply, reply != ""
}

// DraftCommit reads the pending change and drafts its message.
func DraftCommit(ctx context.Context, ask Asker, dir string) Result {
	c, err := Pending(ctx, dir)
	if err != nil {
		return Result{Err: fmt.Errorf("commit: %v", err)}
	}
	if c.Empty() {
		return Result{Note: "nothing to commit (working tree clean)"}
	}
	draft := ""
	if ask != nil {
		reply, err := ask.AskOnce(ctx, CommitPrompt(c, RecentSubjects(ctx, c.Root, 10)))
		if err != nil {
			return Result{Info: c.Preview(), Compose: "/commit -m ", Err: fmt.Errorf("commit: could not draft a message: %v · write one after -m", err)}
		}
		if d, ok := draftText(reply); ok {
			draft = d
		} else {
			return Result{Info: c.Preview(), Compose: "/commit -m ", Note: "the model answered without a message · write one after -m and send it to commit"}
		}
	}
	return Result{
		Info:    c.Preview(),
		Compose: "/commit -m " + draft,
		Note:    "drafted · edit the message, then send it to commit",
	}
}

// CommitWith commits with msg and attaches the proof note.
func CommitWith(ctx context.Context, attach AttachFunc, dir, msg string) Result {
	sha, err := Commit(ctx, dir, msg)
	if err != nil {
		return Result{Err: fmt.Errorf("commit: %v", err)}
	}
	short := sha
	if len(short) > 10 {
		short = short[:10]
	}
	note := "committed " + short
	if attach != nil && attach(ctx, sha) {
		note += " · proof attached · /proof shows it"
	}
	subject, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return Result{Note: note + " · " + subject}
}

// DraftPR plans the pull request and drafts its title and body.
func DraftPR(ctx context.Context, ask Asker, dir string) Result {
	p, err := PlanPR(ctx, dir)
	if err != nil {
		return Result{Err: fmt.Errorf("pr: %v", err)}
	}
	title, body := p.FallbackDraft()
	note := "drafted · nothing is pushed yet · edit it, then send it to push and open the pull request"
	if ask != nil {
		reply, err := ask.AskOnce(ctx, PRPrompt(p))
		if err != nil {
			return Result{Info: p.Preview(), Compose: "/pr create ", Err: fmt.Errorf("pr: could not draft: %v · write a title after create", err)}
		}
		if d, ok := draftText(reply); ok {
			if t, b := ParseDraft(d); t != "" {
				title, body = t, b
			}
		} else {
			note = "the model answered without a draft, so this one is built from the commits · nothing is pushed yet · edit it, then send it"
		}
	}
	compose := "/pr create " + title
	if body != "" {
		compose += "\n" + body
	}
	return Result{
		Info:    p.Preview(),
		Compose: compose,
		Note:    note,
	}
}

// OpenPR is "/pr create": push and open the pull request.
func OpenPR(ctx context.Context, dir, arg string) Result {
	title, body := SplitCreate(arg)
	res, err := CreatePR(ctx, dir, title, body)
	if err != nil {
		return Result{Err: fmt.Errorf("pr: %v", err)}
	}
	if res.Opened {
		return Result{Info: "opened " + res.URL, Note: "pull request opened"}
	}
	return Result{Info: "pushed · open the pull request here: " + res.URL, Note: "branch pushed"}
}

// Fetch is /pr-comments [n] or /issue <n>: gh's answer, shown and shared
// with the model.
func Fetch(ctx context.Context, dir, name, arg string) Result {
	var text string
	var err error
	if name == "/issue" {
		text, err = Issue(ctx, dir, arg)
	} else {
		text, err = PRComments(ctx, dir, strings.TrimPrefix(strings.TrimSpace(arg), "#"))
	}
	if err != nil {
		return Result{Err: err}
	}
	return Result{
		Info:   text,
		Shared: "I fetched this from GitHub with gh:\n<github>\n" + text + "\n</github>",
		Note:   "shared with the model · say what to do with it",
	}
}

// Route runs /commit, /pr, /pr-comments or /issue with its argument,
// the one entry point both front ends call.
func Route(ctx context.Context, ask Asker, attach AttachFunc, dir, name, arg string) Result {
	arg = strings.TrimSpace(arg)
	switch name {
	case "/commit":
		if msg, ok := strings.CutPrefix(arg, "-m"); ok && (msg == "" || msg[0] == ' ' || msg[0] == '\n') {
			return CommitWith(ctx, attach, dir, msg)
		}
		if arg != "" {
			return Result{Err: fmt.Errorf("usage: /commit, or /commit -m <message>")}
		}
		return DraftCommit(ctx, ask, dir)
	case "/pr":
		if rest, ok := strings.CutPrefix(arg, "create"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\n') {
			return OpenPR(ctx, dir, rest)
		}
		if arg != "" {
			return Result{Err: fmt.Errorf("usage: /pr, or /pr create <title> (a newline, then the body)")}
		}
		return DraftPR(ctx, ask, dir)
	case "/pr-comments", "/issue":
		return Fetch(ctx, dir, name, arg)
	}
	return Result{Err: fmt.Errorf("unknown command %s", name)}
}

// Status is the note a front end shows while Route works.
func Status(name, arg string) string {
	arg = strings.TrimSpace(arg)
	switch {
	case name == "/commit" && strings.HasPrefix(arg, "-m"):
		return "committing…"
	case name == "/commit":
		return "drafting a commit message…"
	case name == "/pr" && strings.HasPrefix(arg, "create"):
		return "pushing and opening the pull request…"
	case name == "/pr":
		return "drafting the pull request…"
	}
	return "fetching with gh…"
}
