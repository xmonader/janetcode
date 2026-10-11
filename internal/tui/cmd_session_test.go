package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

// /clear's "new session" acknowledgement was set and then blanked by the
// reset that followed it.
func TestClearAcknowledgesTheNewSession(t *testing.T) {
	h := newHarness(t)
	h.m.command("/clear")
	if !strings.Contains(h.m.status, "new session "+h.m.opts.Session.Meta.ID) {
		t.Fatalf("status after /clear: %q", h.m.status)
	}
}

// A problem opening the resumed session (an unreadable undo file) was
// printed into the transcript that the very next line threw away.
func TestResumeKeepsTheOpenError(t *testing.T) {
	h := newHarness(t)
	dir := dirOf(h.m.opts.Session)
	other, err := session.New(dir, h.m.opts.Cwd, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetTitle("other"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other.UndoPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.m.command("/resume " + other.Meta.ID)
	if h.m.opts.Session.Meta.ID != other.Meta.ID {
		t.Fatalf("did not resume: %s\n%s", h.m.opts.Session.Meta.ID, plain(h.m.View()))
	}
	if !transcriptHas(h.m, other.UndoPath()) {
		t.Fatalf("the undo file error was lost:\n%s", plain(h.m.View()))
	}
}
