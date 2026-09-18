package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// /trajectory used to print the raw event log: one line per event, with
// a tool call and its result on separate lines and nothing joining them.
// It is a ledger now — grouped by turn and step, a call carrying its own
// result and duration.
func TestTrajectoryShowsTheLedgerNotTheRawLog(t *testing.T) {
	h := newHarness(t,
		&llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ID: "c1", Name: "read_file", Input: []byte(`{"path":"a.txt"}`)}}},
			StopReason: llm.StopToolUse,
		},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "alpha it is"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.keys("read the file")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	h.keys("/trajectory")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"turn 1", "Step 1", "USER", "MODEL", "TOOL", "read_file"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the ledger leaves out %q:\n%s", want, v)
		}
	}
	// The raw log is still one word away, and it is a different shape.
	h.keys("/trajectory raw")
	h.enter()
	if raw := plain(h.m.View()); !strings.Contains(raw, "surface") || !strings.Contains(raw, "tool/result") {
		t.Fatalf("/trajectory raw:\n%s", raw)
	}
}

func TestTrajectoryRejectsNonsense(t *testing.T) {
	h := newHarness(t)
	h.keys("/trajectory wat")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "unknown") || !strings.Contains(v, "/trajectory slow") {
		t.Fatalf("a bad argument should say what is accepted:\n%s", v)
	}
}
