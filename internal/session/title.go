package session

import (
	"context"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const titleSystem = "Name this coding conversation. Reply with a title of at most six words, plain text, no quotes or trailing punctuation."

// GenerateTitle asks the model for a short title from the first exchange.
func GenerateTitle(ctx context.Context, p llm.Provider, model string, msgs []llm.Message) (string, error) {
	var sb strings.Builder
	n := 0
	for _, m := range msgs {
		t := strings.TrimSpace(m.Text())
		if t == "" {
			continue
		}
		sb.WriteString(string(m.Role) + ": " + textfmt.TruncateTo(t, 800) + "\n\n")
		if n++; n >= 4 {
			break
		}
	}
	if sb.Len() == 0 {
		return "", nil
	}
	resp, err := p.Stream(ctx, llm.Request{Model: model, System: titleSystem, MaxTokens: 30,
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, sb.String()+"Title:")}}, nil)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(resp.Message.Text())
	title = strings.Trim(textfmt.FirstLine(title, 80), "\"'`* .")
	if len(strings.Fields(title)) > 10 {
		title = strings.Join(strings.Fields(title)[:10], " ")
	}
	return title, nil
}
