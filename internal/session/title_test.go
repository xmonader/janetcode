package session

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// titleProvider captures the prompt instead of calling a model, so the
// test can assert what the title generator was fed.
type titleProvider struct {
	prompt    string
	system    string
	reply     string
	maxTokens int
}

func (p *titleProvider) Name() string { return "test" }

func (p *titleProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(string(m.Role) + ": " + m.Text() + "\n")
	}
	p.prompt, p.system, p.maxTokens = sb.String(), req.System, req.MaxTokens
	reply := p.reply
	if reply == "" {
		reply = "A fine title"
	}
	m := llm.TextMessage(llm.RoleAssistant, reply)
	return &llm.Response{Message: m}, nil
}

// Synthetic context — repository maps, goal rounds — must not reach the
// title prompt: sessions were being named after attachments nobody typed.
func TestGenerateTitleSkipsSyntheticMessages(t *testing.T) {
	repoMap := llm.TextMessage(llm.RoleUser, "[repository map: 2 source files, ranked for this request; 4k tokens]")
	real := llm.TextMessage(llm.RoleUser, "fix the flaky board test")
	reply := llm.TextMessage(llm.RoleAssistant, "I will look at the test.")
	p := &titleProvider{}
	if _, err := GenerateTitle(context.Background(), p, "m", []llm.Message{repoMap, real, reply}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.prompt, "repository map") {
		t.Fatalf("the synthetic attachment reached the title prompt:\n%s", p.prompt)
	}
	if !strings.Contains(p.prompt, "fix the flaky board test") {
		t.Fatalf("the real message never reached the title prompt:\n%s", p.prompt)
	}
}

// A model that answers the conversation instead of naming it must not
// become the session's title: a live run named a session "I don't have
// the ability to execute bash command…". Such replies come back empty so
// the caller falls back to the first prompt.
func TestGenerateTitleRejectsRefusals(t *testing.T) {
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "run ls -la in the repo root")}
	for _, reply := range []string{
		"I don't have the ability to execute bash commands",
		"I'm sorry, but I can't run commands",
		"Sorry, I cannot help with that",
		"As an AI, I cannot execute code",
		"I cannot access your files",
		"Unfortunately I am unable to do that",
		"I'll run that command for you",
		"Here is what I found",
	} {
		p := &titleProvider{reply: reply}
		got, err := GenerateTitle(context.Background(), p, "m", msgs)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("reply %q became title %q", reply, got)
		}
	}
	var p *titleProvider
	for _, reply := range []string{"Title: List repo root files", "Here is a title: List repo root files", "**List repo root files**"} {
		p = &titleProvider{reply: reply}
		if got, _ := GenerateTitle(context.Background(), p, "m", msgs); got != "List repo root files" {
			t.Errorf("reply %q came back as %q", reply, got)
		}
	}
	// The conversation is quoted as material to name, not handed over as
	// a request the model should carry out.
	if !strings.Contains(p.prompt, "<conversation>") || !strings.Contains(p.system, "do not answer") {
		t.Errorf("title prompt does not fence the conversation:\nsystem: %s\nprompt: %s", p.system, p.prompt)
	}
}

// At 30 output tokens a reasoning model spent the budget thinking and
// wrote no title about half the time; the call leaves room to think.
func TestGenerateTitleLeavesRoomToThink(t *testing.T) {
	p := &titleProvider{}
	if _, err := GenerateTitle(context.Background(), p, "m", []llm.Message{llm.TextMessage(llm.RoleUser, "fix the build")}); err != nil {
		t.Fatal(err)
	}
	if p.maxTokens < 200 {
		t.Fatalf("title call allows %d output tokens; a reasoning model needs room to think first", p.maxTokens)
	}
}
