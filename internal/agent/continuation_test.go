package agent

import (
	"context"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestFailedContinuationCanBeRetried(t *testing.T) {
	log := &session.Memory{}
	msg := llm.TextMessage(llm.RoleUser, "finish the interrupted work")
	for _, e := range []session.Event{
		{Type: session.TypeTurnStart, Turn: 1, Extra: session.TurnLogsEnd},
		{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &msg},
	} {
		if _, err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{
		Tools:    tool.NewRegistry(),
		Model:    "m",
		Log:      log,
		Messages: session.FoldSurface(log.Events()),
	}
	a.RestoreFromLog()

	if err := a.ContinueInterrupted(context.Background(), nil); err == nil {
		t.Fatal("continuation without a provider succeeded")
	}
	if it := a.Interrupted(); it == nil || it.Turn != 1 {
		t.Fatalf("failed continuation closed the dead turn before opening its replacement: %+v", it)
	}

	a.Provider = &fake{responses: []*llm.Response{text("continued")}}
	if err := a.ContinueInterrupted(context.Background(), nil); err != nil {
		t.Fatalf("retry after connecting a provider: %v", err)
	}
	if got := a.Messages[len(a.Messages)-1].Text(); got != "continued" {
		t.Fatalf("final message = %q, want the continuation's answer", got)
	}
}
