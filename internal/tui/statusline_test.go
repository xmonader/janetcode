package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/statusline"
)

// runStatusLineNow runs the status line command the way a tick would, and
// hands its answer back; false when the tick would not have run it.
func (h *harness) runStatusLineNow() bool {
	cmd := h.m.runStatusLine()
	if cmd == nil {
		return false
	}
	h.update(cmd())
	return true
}

func TestTheStatusLineCommandIsDrawnUnderTheInput(t *testing.T) {
	opts := fakeOptions(t)
	opts.StatusLine = &statusline.Config{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print("model="+d["model"]["id"], "mode="+d["permission_mode"]); print("second"); print("third")'`}
	h := newHarnessFor(t, opts)
	before := h.m.viewport.Height
	if !h.runStatusLineNow() {
		t.Fatal("the first tick did not run the command")
	}
	view := ansi.Strip(h.m.View())
	model := h.m.opts.Agent.Settings().Model
	if !strings.Contains(view, "model="+model+" mode=ask") || !strings.Contains(view, "second") || strings.Contains(view, "third") {
		t.Fatalf("view:\n%s", view)
	}
	// The two rows come out of the transcript, not off the screen.
	if h.m.viewport.Height != before-2 || strings.Count(view, "\n") != 29 {
		t.Fatalf("viewport %d → %d, %d rows", before, h.m.viewport.Height, strings.Count(view, "\n")+1)
	}
	if h.runStatusLineNow() {
		t.Fatal("ran again with nothing changed")
	}
	h.m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.AutoApprove = true })
	if !h.runStatusLineNow() || !strings.Contains(ansi.Strip(h.m.View()), "mode=yolo") {
		t.Fatalf("a mode change was not picked up:\n%s", ansi.Strip(h.m.View()))
	}
}

func TestAFailingStatusLineIsBlankAndSaysWhy(t *testing.T) {
	opts := fakeOptions(t)
	opts.StatusLine = &statusline.Config{Command: `echo "jq: not found" >&2; exit 127`}
	h := newHarnessFor(t, opts)
	before := h.m.viewport.Height
	h.runStatusLineNow()
	view := ansi.Strip(h.m.View())
	if !strings.Contains(view, "status line command failed: jq: not found") || h.m.viewport.Height != before {
		t.Fatalf("viewport %d → %d, view:\n%s", before, h.m.viewport.Height, view)
	}
}

func TestNoStatusLineConfiguredRunsNothing(t *testing.T) {
	h := newHarnessFor(t, fakeOptions(t))
	if h.runStatusLineNow() {
		t.Fatal("ran a status line nobody configured")
	}
}
