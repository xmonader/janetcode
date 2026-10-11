package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// streamThenCancel streams some answer, then the user cancels: the
// provider returns no response and no usage, as every real one does.
type streamThenCancel struct {
	cancel context.CancelFunc
	text   string
}

func (p *streamThenCancel) Name() string { return "test" }

func (p *streamThenCancel) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit != nil {
		emit(llm.Event{Type: llm.EventThinkingDelta, Text: "let me think about it"})
		emit(llm.Event{Type: llm.EventTextDelta, Text: p.text})
	}
	p.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}

// A streamed answer the user cancelled was paid for — the prompt was
// read and the output generated — yet the session's ↑/↓ counts did not
// move. With no usage from the provider, the spend is estimated from the
// request and what was streamed.
func TestCancelledStreamCountsItsUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &streamThenCancel{cancel: cancel, text: strings.Repeat("a fairly long streamed answer ", 40)}
	a, _ := newAgent(t, &fake{}, nil)
	a.Provider = p
	err := a.Run(ctx, "explain the codebase", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	u := a.UsageSnapshot()
	if u.InputTokens == 0 {
		t.Errorf("input of the cancelled call not counted: %+v", u)
	}
	if u.OutputTokens < 100 {
		t.Errorf("streamed output of the cancelled call not counted: %+v", u)
	}
	if rows := a.UsageByModel(); len(rows) != 1 || rows[0].Usage.OutputTokens != u.OutputTokens {
		t.Errorf("cancelled call not attributed to its model: %+v", rows)
	}
}

// A call cancelled before the provider produced anything has nothing to
// show it was ever processed, and is not billed by guesswork.
func TestCancelledBeforeAnyTokenCountsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &streamThenCancel{cancel: cancel}
	a, _ := newAgent(t, &fake{}, nil)
	a.Provider = &silentCancel{p}
	_ = a.Run(ctx, "hi", nil)
	if u := a.UsageSnapshot(); u != (llm.Usage{}) {
		t.Fatalf("usage invented for a call that never answered: %+v", u)
	}
}

type silentCancel struct{ p *streamThenCancel }

func (s *silentCancel) Name() string { return "test" }

func (s *silentCancel) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	s.p.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}
