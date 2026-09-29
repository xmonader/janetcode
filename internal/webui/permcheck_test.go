package webui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
)

// /permissions check answers in the web what it answers in the terminal:
// what the rules decide about a command before it runs.
func TestWebPermissionsCheck(t *testing.T) {
	s, _, _, _ := cdServer(t)
	s.opts.Agent.AddRule(perm.Denied, perm.Rule{Tool: "bash", Pattern: "rm *"})
	if err := s.runCommand("/permissions check rm -rf build"); err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(s.noticeTexts(), "\n")
	if !strings.Contains(notes, "rm -rf build") || !strings.Contains(notes, "bash(rm *)") {
		t.Errorf("notices = %q, want the command judged by bash(rm *)", notes)
	}
}
