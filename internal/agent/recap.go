package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

const recapSystem = `Write a recap for a user returning to a coding session after a break: where they were. Two to four short sentences, plain text, no markdown headings: what the user was trying to do, what has been done so far, and what the obvious next step or open question is. Name files and commands that matter. Do not call tools, do not ask questions.`

// recapMax caps the recap so a rambling model cannot flood the scrollback.
const recapMax = 1200

// Recap writes a "where was I" summary of the session so far. Like
// AskOnce it records nothing: the text is for the user's eyes only and
// never enters Messages. It sends the most recent turns with long
// messages shortened and no tool schemas, on the cheap model slot when
// one is set.
func (a *Agent) Recap(ctx context.Context) (string, error) {
	s := a.Settings()
	if s.Provider == nil {
		return "", fmt.Errorf("no provider")
	}
	msgs := sideTranscript(a.MessagesSnapshot(), 24, 2000, 400)
	if len(msgs) == 0 {
		return "", fmt.Errorf("nothing to recap yet")
	}
	msgs = askAtEnd(msgs, "Recap this session for me.")
	model := s.Model
	if cheap := a.RouteCheapName(); cheap != "" {
		model = cheap
	}
	resp, err := s.Provider.Stream(ctx, llm.Request{
		Model:     model,
		System:    recapSystem,
		Messages:  msgs,
		MaxTokens: 400,
		Effort:    "low",
	}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("empty reply")
	}
	a.addUsage(model, resp.Usage)
	out := clampRecap(resp.Message.Text())
	if out == "" {
		return "", fmt.Errorf("empty reply")
	}
	return out, nil
}

// askAtEnd puts a request at the end of a trimmed transcript: into the
// last message when that is the user's, else as a new one.
func askAtEnd(msgs []llm.Message, ask string) []llm.Message {
	q := llm.Block{Type: llm.BlockText, Text: ask}
	if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleUser {
		msgs[n-1].Blocks = append(msgs[n-1].Blocks, q)
		return msgs
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{q}})
}

func clampRecap(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > recapMax {
		s = strings.TrimSpace(string(r[:recapMax])) + "…"
	}
	return s
}
