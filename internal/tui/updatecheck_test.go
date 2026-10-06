package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

// The release notice is asked for at start and lands in the transcript;
// an empty answer shows nothing.
func TestUpdateNoticeShown(t *testing.T) {
	here := t.TempDir()
	sess := newSession(t, here)
	ag := &agent.Agent{Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: here}
	asked := 0
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: here, Version: "test",
		UpdateNotice: func(context.Context) string { asked++; return "janetcode v9.9.9 is out" }})
	cmd := m.checkUpdate()
	if cmd == nil {
		t.Fatal("no update check")
	}
	m.Update(cmd())
	if asked != 1 || !strings.Contains(transcriptText(m), "v9.9.9 is out") {
		t.Fatalf("asked %d, transcript:\n%s", asked, transcriptText(m))
	}
	before := transcriptText(m)
	m.Update(updateNoticeMsg(""))
	if transcriptText(m) != before {
		t.Fatal("empty notice changed the transcript")
	}
}
