package tui

import (
	"testing"

	"github.com/xmonader/janetcode/internal/cron"
)

// A /loop with nothing to send is a mistake, not a prompt: "/loop cancel"
// with no id and "/loop 10m" with no prompt each armed a loop that sent the
// word itself to the model every five minutes.
func TestLoopRefusesAMissingPrompt(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Cron = cron.New()
	t.Cleanup(h.m.opts.Agent.Cron.Close)
	for _, line := range []string{"/loop cancel", "/loop 10m", "/loop 10m   ", "/loop 25h ping"} {
		h.m.command(line)
		if jobs := h.m.opts.Agent.Cron.List(); len(jobs) != 0 {
			t.Fatalf("%q armed %d loop(s): %+v", line, len(jobs), jobs)
		}
	}
	if !transcriptHas(h.m, "usage: /loop") {
		t.Fatalf("no usage line:\n%s", plain(h.m.View()))
	}
	h.m.command("/loop 10m check the build")
	jobs := h.m.opts.Agent.Cron.List()
	if len(jobs) != 1 || jobs[0].Prompt != "check the build" {
		t.Fatalf("a loop with a prompt was not armed as typed: %+v", jobs)
	}
}
