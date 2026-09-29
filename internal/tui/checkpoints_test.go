package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestEscEscOpensTheCheckpointsAndEnterPreviewsARestore: after a turn
// that changed a file, esc twice on an idle empty prompt lists its
// checkpoint, and enter on it previews putting the tree back.
func TestEscEscOpensTheCheckpointsAndEnterPreviewsARestore(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: []byte(`{"path":"a.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("write a.txt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.picker != nil {
		t.Fatal("one esc opened the checkpoint list")
	}
	if !strings.Contains(h.m.status, "esc again") {
		t.Fatalf("the first esc did not say what a second would do: %q", h.m.status)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.picker == nil || h.m.picker.title != "Checkpoints" || len(h.m.picker.rows) != 1 {
		t.Fatalf("esc esc did not open the checkpoints: %+v", h.m.picker)
	}
	cov := strings.Join(h.m.picker.rows[0].cells, " ")
	if !strings.Contains(cov, "file copies only") || !strings.Contains(cov, "a.txt") || !strings.Contains(cov, "complete") {
		t.Fatalf("row = %q", cov)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "restoring the work tree to before turn 1") })
}

// TestCheckpointsWithNoneSaysSo: an empty list is a note, not an empty
// picker, and esc esc with nothing to rewind to stays quiet.
func TestCheckpointsWithNoneSaysSo(t *testing.T) {
	h := newHarness(t)
	h.keys("/checkpoints")
	h.enter()
	if h.m.picker != nil || !strings.Contains(h.m.status, "no checkpoints yet") {
		t.Fatalf("picker=%v status=%q", h.m.picker, h.m.status)
	}
	h.m.status = ""
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(h.m.status, "esc again") {
		t.Fatal("offered a rewind with nothing to rewind to")
	}
}
