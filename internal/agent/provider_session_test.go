package agent

import (
	"context"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

type sessionProvider struct{ ids []string }

func (p *sessionProvider) Name() string { return "session-test" }
func (p *sessionProvider) Stream(ctx context.Context, _ llm.Request, _ func(llm.Event)) (*llm.Response, error) {
	p.ids = append(p.ids, llm.SessionID(ctx))
	return text("ok"), nil
}

func TestGatewayConversationIDStableAndDistinct(t *testing.T) {
	p := &sessionProvider{}
	a := &Agent{Provider: p, Model: "m"}
	for i := 0; i < 2; i++ {
		if err := a.Run(context.Background(), "hi", nil); err != nil {
			t.Fatal(err)
		}
	}
	b := &Agent{Provider: p, Model: "m"}
	if err := b.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.ids) != 3 || p.ids[0] == "" || p.ids[0] != p.ids[1] || p.ids[0] == p.ids[2] {
		t.Fatalf("IDs=%v", p.ids)
	}
}

func TestGatewayConversationIDSurvivesResumeAndCoversSideCalls(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	s, err := session.New(dir, root, "test", "m")
	if err != nil {
		t.Fatal(err)
	}
	p := &sessionProvider{}
	a := &Agent{Provider: p, Model: "m"}
	a.SetLog(s)
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AskOnce(context.Background(), "side question"); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := session.Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := &Agent{Provider: p, Model: "m"}
	b.SetLog(reopened)
	if err := b.Run(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range p.ids {
		if id != s.Meta.ID {
			t.Fatalf("ID=%s want saved session ID", id)
		}
	}
}
