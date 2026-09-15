package webui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/plan"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func planServer(t *testing.T, responses ...*llm.Response) (*server, string) {
	t.Helper()
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Provider: &scripted{responses: responses}, Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: root}
	return newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root}), root
}

// waitFor polls until the condition holds: a turn the web UI started
// runs on its own goroutine.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// sheetText is the body of the last sheet entry the server rendered.
func (s *server) sheetText(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Kind == "sheet" {
			return s.entries[i].Text
		}
	}
	t.Fatal("no sheet was rendered")
	return ""
}

func (s *server) noticeTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.entries {
		if e.Kind == "notice" {
			out = append(out, e.Text)
		}
	}
	return out
}

// The web UI has no cursor in a file, so a comment names a line; every
// other step of the review is the same command the terminal runs.
func TestWebPlanReviewCommentsAndChanges(t *testing.T) {
	s, root := planServer(t, &llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "## Plan\n1. rename Parser to Parse\n2. update the tests"),
		StopReason: llm.StopEndTurn,
	})
	if _, err := plan.Write(root, "## Plan\n1. rename Parser\n2. update the tests"); err != nil {
		t.Fatal(err)
	}
	s.opts.Agent.SetPlanMode(true)

	if err := s.runCommand("/plan review"); err != nil {
		t.Fatal(err)
	}
	sheet := s.sheetText(t)
	for _, want := range []string{"1 │ ## Plan", "2 │ 1. rename Parser", "3 │ 2. update the tests"} {
		if !strings.Contains(sheet, want) {
			t.Fatalf("the plan sheet is missing %q:\n%s", want, sheet)
		}
	}

	for _, cmd := range []string{"/plan comment 2 rename it to what?", "/plan comment plan.md:3 which tests?"} {
		if err := s.runCommand(cmd); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(s.opts.Agent.PlanComments()); n != 2 {
		t.Fatalf("the web UI collected %d comments", n)
	}
	if err := s.runCommand("/plan review"); err != nil {
		t.Fatal(err)
	}
	if sheet := s.sheetText(t); !strings.Contains(sheet, "↳ which tests?") {
		t.Fatalf("the sheet does not show the comments:\n%s", sheet)
	}

	if err := s.runCommand("/plan changes"); err != nil {
		t.Fatal(err)
	}
	if !s.opts.Agent.PlanMode {
		t.Fatal("asking for changes dropped out of plan mode")
	}
	if n := len(s.opts.Agent.PlanComments()); n != 0 {
		t.Fatalf("%d comments survived being sent", n)
	}
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running
	})
	var got string
	for _, m := range s.opts.Agent.Messages {
		if m.Role == llm.RoleUser {
			got = m.Text()
		}
	}
	for _, want := range []string{"rename it to what?", "which tests?", "1. rename Parser", agent.PlanRevisePrompt} {
		if !strings.Contains(got, want) {
			t.Fatalf("the comments did not reach the model as one message (%q missing):\n%s", want, got)
		}
	}
	// The revision rewrote the file; it did not add a second plan.
	body, err := plan.Read(root)
	if err != nil || !strings.Contains(body, "rename Parser to Parse") || strings.Count(body, "## Plan") != 1 {
		t.Fatalf("plan file = %q err = %v", body, err)
	}
}

func TestWebPlanApproveClearsTheComments(t *testing.T) {
	s, root := planServer(t, &llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "executed"),
		StopReason: llm.StopEndTurn,
	})
	plan.Write(root, "## Plan\n1. rename Parser")
	s.opts.Agent.SetPlanMode(true)
	if err := s.runCommand("/plan comment 2 never sent"); err != nil {
		t.Fatal(err)
	}
	if err := s.runCommand("/plan approve"); err != nil {
		t.Fatal(err)
	}
	if s.opts.Agent.PlanMode {
		t.Fatal("still in plan mode after approve")
	}
	if n := len(s.opts.Agent.PlanComments()); n != 0 {
		t.Fatalf("%d comments survived approval", n)
	}
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running
	})
	notes := strings.Join(s.noticeTexts(), "\n")
	if !strings.Contains(notes, plan.Name) {
		t.Fatalf("approve does not name the plan file:\n%s", notes)
	}
}

func TestWebPlanTurnWritesThePlanFile(t *testing.T) {
	s, root := planServer(t, &llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "Looked.\n\n## Plan\n1. rename Parser"),
		StopReason: llm.StopEndTurn,
	})
	if err := s.runCommand("/plan rename the parser"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, err := plan.Read(root)
		return err == nil
	})
	body, _ := plan.Read(root)
	if !strings.HasPrefix(body, "## Plan") || strings.Contains(body, "Looked.") {
		t.Fatalf("plan file = %q", body)
	}
}
