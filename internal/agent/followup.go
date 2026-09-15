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
	msgs := followupMessages(a.MessagesSnapshot())
	if len(msgs) == 0 {
		return "", nil
	}
	model := s.Model
	if cheap := a.RouteCheapName(); cheap != "" {
		model = cheap
	}
	resp, err := s.Provider.Stream(ctx, llm.Request{
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
	a.addUsage(resp.Usage)
	return clampFollowup(resp.Message.Text()), nil
}

func followupMessages(src []llm.Message) []llm.Message {
	const keep = 6
	if len(src) > keep {
		src = src[len(src)-keep:]
	}
	out := make([]llm.Message, 0, len(src))
	for _, m := range src {
		var blocks []llm.Block
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockThinking:
				continue
			case llm.BlockText:
				t := strings.TrimSpace(b.Text)
				if t == "" {
					continue
				}
				if len(t) > 1500 {
					t = t[:1500]
				}
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
			case llm.BlockToolUse:
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "called " + b.Name})
			case llm.BlockToolResult:
				t := strings.TrimSpace(b.Text)
				if len(t) > 400 {
					t = t[:400]
				}
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, llm.Message{Role: m.Role, Blocks: blocks})
	}
	return out
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
