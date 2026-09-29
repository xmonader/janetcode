package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// /commit in the web UI drafts into the composer through a "compose"
// event that is not replayed, and sending the draft commits.
func TestWebCommitDraftsIntoComposer(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_COMMITTER_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": os.DevNull} {
		t.Setenv(k, v)
	}
	s, dir := planServer(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "fix: web draft"), StopReason: llm.StopEndTurn})
	gitIn(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "start")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644)

	ch := make(chan envelope, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	if err := s.runCommand("/commit"); err != nil {
		t.Fatal(err)
	}
	var compose string
	deadline := time.After(5 * time.Second)
	for compose == "" {
		select {
		case e := <-ch:
			if e.Type == "compose" {
				compose = e.Compose
			}
		case <-deadline:
			t.Fatal("no compose event")
		}
	}
	if compose != "/commit -m fix: web draft" {
		t.Fatalf("compose = %q", compose)
	}
	s.mu.Lock()
	for _, e := range s.log {
		if e.Type == "compose" {
			t.Error("compose kept in the replay log")
		}
	}
	s.mu.Unlock()
	if err := s.runCommand(compose); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gitOutT(t, dir, "log", "-1", "--format=%s") == "fix: web draft" })
}
