package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/tool"
)

// The jobs and subagents lists are drawn in a box that does not scroll:
// with more rows than the window has, the highlight walked off the bottom
// and the key line went with it.
func TestJobsOverlayKeepsTheHighlightOnScreen(t *testing.T) {
	h := newHarness(t)
	jobs := &tool.Jobs{Dir: t.TempDir()}
	h.m.opts.Jobs = jobs
	const n = 15
	for i := 0; i < n; i++ {
		if _, err := jobs.Start(h.m.opts.Cwd, fmt.Sprintf("echo job%02d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	h.m.command("/jobs")
	if h.m.ov.kind != overlayJobs {
		t.Fatal("overlay not open")
	}
	for i := 0; i < n-1; i++ {
		h.update(tea.KeyMsg{Type: tea.KeyDown})
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "▸ job 15") {
		t.Fatalf("highlighted last job is off screen:\n%s", v)
	}
	if !strings.Contains(v, "esc close") {
		t.Fatalf("key line is off screen:\n%s", v)
	}
	if got := len(strings.Split(v, "\n")); got > 30 {
		t.Fatalf("view is %d lines on a 30-line terminal", got)
	}
	// And back to the top.
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	if v := plain(h.m.View()); !strings.Contains(v, "▸ job 1 ") {
		t.Fatalf("wrapped highlight is off screen:\n%s", v)
	}
}

// A /jobs or /agents argument naming nothing used to open the list in
// silence: "/jobs kill" with no number became a pane for job "kill", and
// a mistyped id showed the list as though it had been found.
func TestJobsAndAgentsSayWhatTheyCouldNotFind(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Jobs = &tool.Jobs{Dir: t.TempDir()}
	for _, line := range []string{"/jobs kill", "/jobs stdin"} {
		h.m.command(line)
		if h.m.ov.kind != "" {
			t.Fatalf("%q opened the overlay instead of saying how to use it", line)
		}
		if !transcriptHas(h.m, "usage: "+line+" <n>") {
			t.Fatalf("%q: no usage line:\n%s", line, plain(h.m.View()))
		}
	}
	h.m.command("/jobs 99")
	if v := plain(h.m.View()); h.m.ov.kind != overlayJobs || !strings.Contains(v, "no job 99") {
		t.Fatalf("/jobs 99:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	attachTask(h)
	h.m.command("/agents zz")
	if v := plain(h.m.View()); h.m.ov.kind != overlayAgents || !strings.Contains(v, "no subagent zz") {
		t.Fatalf("/agents zz:\n%s", v)
	}
}
