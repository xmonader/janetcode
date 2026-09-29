package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// /commit drafts with the session's model, puts the draft in the input
// box, and sending it commits exactly that message.
func TestCommitDraftsIntoInputThenCommits(t *testing.T) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@example.com")
	}
	opts := fakeOptions(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "```\nfeat: add b\n```"), StopReason: llm.StopEndTurn})
	dir := opts.Cwd
	gitIn(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "chore: start")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee\n"), 0o644)

	h := newHarnessFor(t, opts)
	h.keys("/commit")
	h.enter()
	h.pump(func() bool { return strings.HasPrefix(h.m.input.Value(), "/commit -m") })
	if got := h.m.input.Value(); got != "/commit -m feat: add b" {
		t.Fatalf("input = %q", got)
	}
	reqs := opts.Agent.Provider.(*scripted).reqs()
	last := reqs[len(reqs)-1].Messages
	if p := last[len(last)-1].Text(); !strings.Contains(p, "+bee") || !strings.Contains(p, "chore: start") {
		t.Fatalf("drafting prompt lacks the diff or the style:\n%s", p)
	}
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "committed ") })
	if got := gitIn(t, dir, "log", "-1", "--format=%s"); got != "feat: add b" {
		t.Fatalf("HEAD subject = %q", got)
	}
	if got := gitIn(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "b.txt" {
		t.Fatalf("committed files = %q", got)
	}
}

// A draft never overwrites what the user has typed since asking for it.
func TestGitflowDraftKeepsTypedInput(t *testing.T) {
	h := newHarness(t)
	h.keys("half a thought")
	msg := gitflowMsg{}
	msg.Compose = "/commit -m x"
	h.update(msg)
	if got := h.m.input.Value(); got != "half a thought" {
		t.Fatalf("input = %q", got)
	}
	if !strings.Contains(plain(h.m.View()), "/commit -m x") {
		t.Fatal("the draft was not shown in the transcript instead")
	}
}
