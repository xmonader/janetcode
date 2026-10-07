package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type askFunc func(string) (string, error)

func (f askFunc) AskOnce(_ context.Context, q string) (string, error) { return f(q) }

// What deepseek-v4.1-flash answered to the pull request prompt, live: a
// tool call written as text. It went into the input box as the title.
const dsmlReply = "<｜DSML｜ calls>\n<｜DSML｜ invoke name=\"bash\">\n<｜DSML｜ parameter name=\"command\" string=\"true\">git log -1 --stat</｜DSML｜ parameter>\n</｜DSML｜ invoke>\n</｜DSML｜ calls>"

func TestDraftPRIgnoresAToolCallReply(t *testing.T) {
	GH = "janetcode-no-such-gh"
	t.Cleanup(func() { GH = "gh" })
	dir, _ := withRemote(t)
	r := Route(context.Background(), askFunc(func(string) (string, error) { return dsmlReply, nil }), nil, dir, "/pr", "")
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Compose != "/pr create feat: X" {
		t.Fatalf("compose = %q, want the fallback built from the commit", r.Compose)
	}
	if !strings.Contains(r.Note, "without a draft") {
		t.Fatalf("note = %q", r.Note)
	}
}

func TestDraftCommitIgnoresAToolCallReply(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.go", "package a\n// y\n")
	r := Route(context.Background(), askFunc(func(string) (string, error) {
		return "<tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"git diff\"}}</tool_call>", nil
	}), nil, dir, "/commit", "")
	if r.Compose != "/commit -m " || !strings.Contains(r.Note, "without a message") {
		t.Fatalf("result = %+v", r)
	}
}

func TestRouteCommitDraftThenCommit(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.go", "package a\n// y\n")
	var asked string
	r := Route(context.Background(), askFunc(func(q string) (string, error) { asked = q; return "fix: y", nil }), nil, dir, "/commit", "")
	if r.Compose != "/commit -m fix: y" || !strings.Contains(r.Info, "M a.go") {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(asked, "no tools for this reply") {
		t.Fatal("the prompt does not say there are no tools")
	}
	attached := ""
	r = Route(context.Background(), nil, func(_ context.Context, sha string) bool { attached = sha; return true }, dir, "/commit", strings.TrimPrefix(r.Compose, "/commit"))
	if r.Err != nil || !strings.Contains(r.Note, "proof attached") || !strings.Contains(r.Note, "fix: y") {
		t.Fatalf("result = %+v", r)
	}
	if attached != git(t, dir, "rev-parse", "HEAD") {
		t.Fatal("proof was not attached to the new commit")
	}
}

func TestRouteDraftErrorLeavesAnEmptyCommand(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.go", "package a\n// y\n")
	r := Route(context.Background(), askFunc(func(string) (string, error) { return "", errors.New("503") }), nil, dir, "/commit", "")
	if r.Compose != "/commit -m " || r.Err == nil {
		t.Fatalf("result = %+v", r)
	}
	// "/commitx" and "/pr createx" are not the commit and create verbs.
	if r := Route(context.Background(), nil, nil, dir, "/commit", "-mx"); r.Err == nil || !strings.Contains(r.Err.Error(), "usage") {
		t.Fatalf("-mx: %+v", r)
	}
}

func TestFallbackDraftForManyCommits(t *testing.T) {
	p := PRPlan{Branch: "feat/retry-loop", Commits: []string{"bbb feat: second", "aaa feat: first"}}
	title, body := p.FallbackDraft()
	if title != "feat: retry loop" || body != "- feat: first\n- feat: second" {
		t.Fatalf("%q / %q", title, body)
	}
}
