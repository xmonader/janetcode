package webui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The page opened on a session whose last turn died says so, and /recover
// carries the turn on without a prompt.
func TestWebRecoverCarriesOnTheDeadTurn(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	user := llm.TextMessage(llm.RoleUser, "do the thing")
	sess.Append(session.Event{Type: session.TypeTurnStart, Turn: 1, Extra: session.TurnLogsEnd})
	sess.Append(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &user})
	prov := &scripted{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "carried on"), StopReason: llm.StopEndTurn}}}
	ag := &agent.Agent{Provider: prov, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root,
		Messages: session.FoldSurface(sess.Events())}
	ag.RestoreFromLog()
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root})

	text := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		var b strings.Builder
		for _, e := range s.entries {
			b.WriteString(e.Text + "\n")
		}
		return b.String()
	}
	if !strings.Contains(text(), "/recover carries it on") {
		t.Fatalf("no notice on opening:\n%s", text())
	}
	if err := s.runCommand("/recover"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(text(), "carried on") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(text(), "carried on") {
		t.Fatalf("the turn was not carried on:\n%s", text())
	}
	if session.FindInterrupted(sess.Events()) != nil {
		t.Fatal("still interrupted after /recover")
	}
}
