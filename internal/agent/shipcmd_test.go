package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// /commit commits what the session wrote, with a message drafted from
// the diff and the change's stated intent, and leaves the person's own
// edit alone.
func TestCommitCommandCommitsTheSessionsWork(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add the greeting file."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: []byte(`{"path":"hello.txt","content":"hello\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
		text("feat: add a greeting\n\nSo there is something to read."),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "add", ".")
	gitT(t, root, "commit", "-q", "-m", "init")
	if err := a.Run(context.Background(), "add a greeting", nil); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("my own edit\n"), 0o644)

	res, err := a.CommitCommand(context.Background(), root, "")
	if err != nil || res.Error != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "feat: add a greeting · 1 file") {
		t.Fatalf("notes = %v", res.Notes)
	}
	if !strings.Contains(strings.Join(res.Tips, "\n"), "left alone: a.txt") {
		t.Fatalf("tips = %v", res.Tips)
	}
	if got := gitT(t, root, "show", "--name-only", "--format=%B", "HEAD"); !strings.HasPrefix(got, "feat: add a greeting\n\nSo there is something to read.") || !strings.HasSuffix(got, "hello.txt") {
		t.Fatalf("commit = %q", got)
	}
	draft := f.requests[len(f.requests)-1]
	if draft.System != commitSystem || len(draft.Tools) != 0 || !strings.Contains(draft.Messages[0].Text(), "- Add the greeting file.") || !strings.Contains(draft.Messages[0].Text(), "new file hello.txt") {
		t.Fatalf("draft request = %+v", draft)
	}

	// Nothing of the session's is left: /commit says so and commits nothing.
	res, _ = a.CommitCommand(context.Background(), root, "")
	if !strings.Contains(res.Error, "nothing to commit") {
		t.Fatalf("second commit = %+v", res)
	}
}

func TestParsePR(t *testing.T) {
	title, body := parsePR("TITLE: Fix the parser\nBODY:\nIt choked on tabs.\n\nTested with go test.")
	if title != "Fix the parser" || body != "It choked on tabs.\n\nTested with go test." {
		t.Fatalf("%q %q", title, body)
	}
	if title, _ := parsePR("Fix things"); title != "" {
		t.Fatalf("unshaped reply parsed as %q", title)
	}
}
