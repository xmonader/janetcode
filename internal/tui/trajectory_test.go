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
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
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

// A bare /trajectory is a browser, not a printout: the ledger with a
// timing strip, a highlight that opens the inspected record, a
// projection that cycles, a slowest-first ordering, and a search that
// becomes the list's filter.
func TestTrajectoryBrowser(t *testing.T) {
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
	if h.m.ov.kind != overlayTrajectory {
		t.Fatalf("bare /trajectory should open the browser, kind=%q", h.m.ov.kind)
	}
	v := plain(h.m.View())
	for _, want := range []string{"sequence", "input", "model", "tools", "turn 1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the browser leaves out %q:\n%s", want, v)
		}
	}

	// enter inspects the highlighted record; esc is back to the ledger.
	h.update(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.ov.child == "" {
		t.Fatal("enter should open the record's detail")
	}
	if dv := plain(h.m.View()); !strings.Contains(dv, "log seq") {
		t.Fatalf("the detail should read like the inspector:\n%s", dv)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.child != "" {
		t.Fatal("esc should close the detail")
	}

	// m cycles the projection; s flips to slowest first.
	h.keys("m")
	if h.m.traj.mode.String() != "duration" {
		t.Fatalf("m should cycle to duration, got %s", h.m.traj.mode)
	}
	h.keys("s")
	if !h.m.traj.slow {
		t.Fatal("s should order slowest first")
	}
	if sv := plain(h.m.View()); !strings.Contains(sv, "slowest first") {
		t.Fatalf("the slow ordering should say so:\n%s", sv)
	}
	h.keys("s")

	// / searches; the typed words become the list's filter, and the
	// chosen record is the one the highlight lands on.
	h.keys("/")
	if h.m.picker == nil {
		t.Fatal("/ should open the search picker")
	}
	h.keys("read_file")
	h.enter()
	if h.m.traj.filter != "read_file" {
		t.Fatalf("the search should become the filter, got %q", h.m.traj.filter)
	}
	fv := plain(h.m.View())
	if !strings.Contains(fv, `matching "read_file"`) {
		t.Fatalf("the filtered list should say what matches:\n%s", fv)
	}
	h.keys("c")
	if h.m.traj.filter != "" {
		t.Fatalf("c should clear the filter, got %q", h.m.traj.filter)
	}

	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind != "" {
		t.Fatalf("esc should close the browser, kind=%q", h.m.ov.kind)
	}
}
