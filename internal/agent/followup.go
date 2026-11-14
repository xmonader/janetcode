package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

const followupSystem = `Suggest the user's next prompt in this coding session. Reply with 2 to 12 words only: the prompt they would type next. No quotes, no punctuation wrapping, no explanation.`

// SuggestFollowup is a cheap side query for the ghost-text next prompt.
// It does not send the session system prompt or tool schemas.
func (a *Agent) SuggestFollowup(ctx context.Context) (string, error) {
	s := a.Settings()
	if s.Provider == nil {
		return "", fmt.Errorf("no provider")
	}
	msgs := sideTranscript(a.MessagesSnapshot(), 6, 1500, 400)
	if len(msgs) == 0 {
		return "", nil
	}
	model := s.Model
	if cheap := a.RouteCheapName(); cheap != "" {
		model = cheap
	}
	resp, err := a.stream(ctx, s.Provider, llm.Request{
		Model:     model,
		System:    followupSystem,
		Messages:  msgs,
		MaxTokens: 64,
		Effort:    "low",
	}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	a.addUsage(model, resp.Usage)
	return clampFollowup(resp.Message.Text()), nil
}

func clampFollowup(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Trim(s, `"'`)
	words := strings.Fields(s)
	if len(words) < 2 {
		return ""
	}
	if len(words) > 12 {
		words = words[:12]
	}
	return strings.Join(words, " ")
}
