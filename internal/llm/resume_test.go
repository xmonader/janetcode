package llm

import (
	"context"
	"strings"
	"testing"
)

// resumeFake plays one scripted attempt per call: some text, then either
// an answer or a break (a discard, as a provider emits before retrying
// fresh). It records the requests it was sent.
type resumeFake struct {
	attempts []resumeAttempt
	reqs     []Request
}

type resumeAttempt struct {
	thinking string
	text     string
	tool     bool
	breaks   bool
	err      error
}

func (f *resumeFake) Name() string { return "fake" }

func (f *resumeFake) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	for {
		f.reqs = append(f.reqs, req)
		a := f.attempts[0]
		if len(f.attempts) > 1 {
			f.attempts = f.attempts[1:]
		}
		if a.err != nil {
			return nil, a.err
		}
		if a.thinking != "" {
			emit(Event{Type: EventThinkingDelta, Text: a.thinking})
		}
		if a.text != "" {
			emit(Event{Type: EventTextDelta, Text: a.text})
		}
		if a.tool {
			emit(Event{Type: EventToolUseStart, Block: &Block{ID: "t1", Name: "read"}})
		}
		if !a.breaks {
			return &Response{Message: TextMessage(RoleAssistant, a.text), StopReason: StopEndTurn}, nil
		}
		// What every provider does with a truncated stream: say the
		// partial is void, then retry fresh — unless the caller is gone.
		emit(Event{Type: EventStreamDiscard})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func resumeRun(t *testing.T, p Provider) (*Response, string, []EventType) {
	t.Helper()
	var screen strings.Builder
	var types []EventType
	resp, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 8000, Messages: []Message{TextMessage(RoleUser, "go")}}, func(ev Event) {
		types = append(types, ev.Type)
		switch ev.Type {
		case EventTextDelta:
			screen.WriteString(ev.Text)
		case EventStreamDiscard:
			screen.Reset()
		}
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	return resp, screen.String(), types
}

var resumeLong = strings.Repeat("word ", 60) // 300 characters

// A reply that broke mid-text is continued from its partial: the second
// request ends with the partial as a prefilled assistant message, the
// screen keeps the partial, and the response is the whole reply.
func TestResumeContinuesBrokenText(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{{text: resumeLong, breaks: true}, {text: " and the rest."}}}
	r := Resume(f, ResumeConfig{})
	resp, screen, types := resumeRun(t, r)
	want := strings.TrimRight(resumeLong, " ") + " and the rest."
	if got := resp.Message.Text(); got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	if !strings.HasSuffix(screen, " and the rest.") || !strings.HasPrefix(screen, "word word") {
		t.Fatalf("screen = %q", screen)
	}
	for _, ty := range types {
		if ty == EventStreamDiscard {
			t.Fatal("a resumed reply must not tell the screen to drop the partial")
		}
	}
	if len(f.reqs) != 2 {
		t.Fatalf("requests = %d", len(f.reqs))
	}
	second := f.reqs[1]
	last := second.Messages[len(second.Messages)-1]
	if !second.Prefill || last.Role != RoleAssistant || last.Text() != strings.TrimRight(resumeLong, " ") {
		t.Fatalf("second request: prefill=%v last=%+v", second.Prefill, last)
	}
	if second.MaxTokens >= 8000 || second.MaxTokens < 1024 {
		t.Fatalf("max tokens = %d, want reduced by the partial", second.MaxTokens)
	}
	if s := r.(*resumer).ResumeStats(); s.Resumed != 1 || s.SavedChars != len(strings.TrimRight(resumeLong, " ")) {
		t.Fatalf("stats = %+v", s)
	}
}

// A partial of a few words is cheaper to regenerate than to steer with:
// the provider's own fresh retry stands.
func TestResumeShortPartialRegenerates(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{{text: "Sure, ", breaks: true}, {text: "fresh answer"}}}
	resp, screen, _ := resumeRun(t, Resume(f, ResumeConfig{}))
	if resp.Message.Text() != "fresh answer" || screen != "fresh answer" || f.reqs[1].Prefill {
		t.Fatalf("response = %q screen = %q prefill = %v", resp.Message.Text(), screen, f.reqs[1].Prefill)
	}
}

// A tool call cut off mid-arguments is not continued.
func TestResumeSkipsToolCalls(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{{text: resumeLong, tool: true, breaks: true}, {text: "fresh"}}}
	resp, _, _ := resumeRun(t, Resume(f, ResumeConfig{}))
	if resp.Message.Text() != "fresh" || f.reqs[1].Prefill {
		t.Fatalf("response = %q prefill = %v", resp.Message.Text(), f.reqs[1].Prefill)
	}
}

// Anthropic refuses a prefill once the model has reasoned; a provider
// that keeps thinking (DeepSeek prefix completion) is continued anyway.
func TestResumeThinking(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{{thinking: "hmm", text: resumeLong, breaks: true}, {text: "fresh"}}}
	resumeRun(t, Resume(f, ResumeConfig{}))
	if f.reqs[1].Prefill {
		t.Fatal("thinking was resumed on a provider that refuses it")
	}
	g := &resumeFake{attempts: []resumeAttempt{{thinking: "hmm", text: resumeLong, breaks: true}, {text: " end"}}}
	resumeRun(t, Resume(g, ResumeConfig{KeepsThinking: true}))
	if !g.reqs[1].Prefill {
		t.Fatal("a provider that keeps thinking was not resumed")
	}
}

// The number of resumes per call is bounded; past it, the provider's own
// fresh retry of the last continuation runs, and the screen is given
// the prefix back after the discard.
func TestResumeBounded(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{
		{text: resumeLong, breaks: true},
		{text: resumeLong, breaks: true},
		{text: " tail"},
	}}
	resp, screen, _ := resumeRun(t, Resume(f, ResumeConfig{Max: 1}))
	prefix := strings.TrimRight(resumeLong, " ")
	if resp.Message.Text() != prefix+" tail" || screen != prefix+" tail" {
		t.Fatalf("response = %q\nscreen = %q", resp.Message.Text(), screen)
	}
	if len(f.reqs) != 3 || !f.reqs[2].Prefill {
		t.Fatalf("requests = %d", len(f.reqs))
	}
}

// A provider that rejects the prefill (a 400) costs one request: the
// reply is regenerated from scratch and the wrapper stops trying.
func TestResumeRefusedFallsBack(t *testing.T) {
	f := &resumeFake{attempts: []resumeAttempt{
		{text: resumeLong, breaks: true},
		{err: hedgeStatus(400)},
		{text: "fresh"},
	}}
	r := Resume(f, ResumeConfig{})
	resp, screen, _ := resumeRun(t, r)
	if resp.Message.Text() != "fresh" || screen != "fresh" {
		t.Fatalf("response = %q screen = %q", resp.Message.Text(), screen)
	}
	if f.reqs[2].Prefill {
		t.Fatal("the fallback still prefilled")
	}
	if s := r.(*resumer).ResumeStats(); s.Refused != 1 {
		t.Fatalf("stats = %+v", s)
	}
	g := &resumeFake{attempts: []resumeAttempt{{text: resumeLong, breaks: true}, {text: "again"}}}
	r.(*resumer).inner = g
	resumeRun(t, r)
	if g.reqs[1].Prefill {
		t.Fatal("a provider that refused was asked again")
	}
}

// A continuation that answers with a tool call and no text still gets the
// partial back in front, after any reasoning.
func TestStitchResumed(t *testing.T) {
	resp := &Response{Message: Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "r"}, {Type: BlockToolUse, ID: "x"}}}}
	stitchResumed(resp, "before")
	b := resp.Message.Blocks
	if len(b) != 3 || b[0].Type != BlockThinking || b[1].Text != "before" || b[2].Type != BlockToolUse {
		t.Fatalf("blocks = %+v", b)
	}
}
