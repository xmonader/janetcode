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
	prompt string
}

func (p *titleProvider) Name() string { return "test" }

func (p *titleProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(string(m.Role) + ": " + m.Text() + "\n")
	}
	p.prompt = sb.String()
	m := llm.TextMessage(llm.RoleAssistant, "A fine title")
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
