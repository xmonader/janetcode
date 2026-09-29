package webui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// /resume in the web lists this directory's sessions, opens one by its
// number, and with fork continues in a copy of it: the typed way on the
// terminal has always had when another janetcode holds a session.
func TestWebResumeListsOpensAndForks(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", data)
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	here := t.TempDir()
	old, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	q := llm.TextMessage(llm.RoleUser, "the old question")
	if _, err := old.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Turn: 1, Message: &q}); err != nil {
		t.Fatal(err)
	}
	cur, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Tools: tool.NewRegistry(), Model: "m", Log: cur, Root: here}
	cd := agent.NewCD(here)
	cd.Bind(tool.NewLocal(here), ag, cur)
	s := newServer(context.Background(), Options{Agent: ag, Session: cur, Provider: "fake", Cwd: here, CD: cd})

	if err := s.runCommand("/resume"); err != nil {
		t.Fatal(err)
	}
	if list := s.sheetText(t); !strings.Contains(list, old.Meta.ID) || !strings.Contains(list, " 1  ") {
		t.Fatalf("/resume listing = %q, want %s numbered 1", list, old.Meta.ID)
	}

	if err := s.runCommand("/resume 1 fork"); err != nil {
		t.Fatal(err)
	}
	got := s.sess().Meta.ID
	if got == old.Meta.ID || got == cur.Meta.ID {
		t.Fatalf("/resume 1 fork is on %s, want a new copy of %s", got, old.Meta.ID)
	}
	if _, msgs, err := session.Open(dir, got); err != nil || len(msgs) == 0 || !strings.Contains(msgs[0].Text(), "the old question") {
		t.Fatalf("the copy does not hold the old conversation: %v %v", msgs, err)
	}
	if filepath.Dir(s.sess().Path()) != dir {
		t.Fatalf("the copy is outside the sessions directory: %s", s.sess().Path())
	}
}
