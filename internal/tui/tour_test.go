package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// writeCall is one write_file call creating path with content.
func writeCall(id, path, content string) llm.Block {
	in, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: "write_file", Input: in}
}

// A turn that changed more than one hunk ends with the tour's line;
// /changes carries the debt until /tour has shown the turn.
func TestTourLineAndDebt(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Two helpers."},
			writeCall("w1", "a.go", "package m\n\nfunc A() int { return 1 }\n"),
			writeCall("w2", "b.go", "package m\n\nfunc B() int { return 2 }\n"),
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 120})
	h.m.opts.Agent.AutoApprove = true
	h.keys("add helpers")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "review: 2 hunks (2 behaviour) · /tour") {
		t.Fatalf("no tour line after the turn:\n%s", v)
	}
	h.keys("/changes")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "comprehension debt: 6 lines approved unread · 6 in .") {
		t.Fatalf("/changes lacks the debt:\n%s", v)
	}
	h.keys("/tour")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"review tour of turn 1", "[behaviour] a.go", "[behaviour] b.go", "intent: Two helpers."} {
		if !strings.Contains(v, want) {
			t.Fatalf("/tour lacks %q:\n%s", want, v)
		}
	}
	if line := h.m.opts.Agent.DebtLine(h.m.opts.Cwd); line != "" {
		t.Fatalf("debt after /tour = %q", line)
	}
}

// Approving at a prompt that showed the whole diff is reading it; a diff
// too long for the prompt, approved without expanding it, is not.
func TestApprovalDebt(t *testing.T) {
	var long strings.Builder
	long.WriteString("package m\n\n")
	for i := range 80 {
		fmt.Fprintf(&long, "var v%d = %d\n", i, i)
	}
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{writeCall("w1", "short.go", "package m\n\nvar x = 1\n")}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{writeCall("w2", "long.go", long.String())}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	h.keys("write them")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	h.keys("y")
	h.pump(func() bool { return h.m.pending != nil && h.m.pending.call.ID == "w2" })
	h.keys("y")
	h.pump(func() bool { return !h.m.turn.running })
	line := h.m.opts.Agent.DebtLine(h.m.opts.Cwd)
	if !strings.Contains(line, "82 lines approved unread") {
		t.Fatalf("debt = %q, want only long.go's 82 lines", line)
	}
}
