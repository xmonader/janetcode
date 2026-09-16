package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/xmonader/janetcode/internal/llm"
)

const maxAdvisorUses = 3

const advisorSystem = `You are advising a coding agent that is stuck. You have NO tools and cannot edit files. From the transcript, reply with: 1) what to do next (concrete), 2) likely pitfalls, 3) how to verify (command if obvious). Be directive and short (under ~400 words). Do not write full file dumps.`

// AdvisorTool is a consult: the executor calls it when stuck. The
// harness forwards the recent transcript. It stays deferred unless
// AdvisorModel is set, so the schema is not on every request.
type AdvisorTool struct{ Parent *Agent }

func (*AdvisorTool) Name() string { return "advisor" }
func (*AdvisorTool) Description() string {
	return "Ask for a short directive when stuck: next step, pitfalls, how to verify. Omit question if you cannot say why. At most 3 uses per turn. Does not edit or run tools."
}
func (*AdvisorTool) Mutating() bool { return false }
func (*AdvisorTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"question":{"type":"string","description":"Optional focus; omit if you cannot say why you are stuck"}},
"additionalProperties":false}`)
}

func (t *AdvisorTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if t == nil || t.Parent == nil {
		return "", fmt.Errorf("advisor: no parent")
	}
	n := atomic.AddInt32(&t.Parent.advisorUses, 1)
	if n > maxAdvisorUses {
		return "", fmt.Errorf("advisor already used %d times this turn; use a tool or finish", maxAdvisorUses)
	}
	var in struct {
		Question string `json:"question"`
	}
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	q := strings.TrimSpace(in.Question)
	if q == "" {
		q = "The executor asked for a general consult. Give the best next plan from the transcript."
	}
	return t.Parent.Advise(ctx, q)
}

// Advise is the stuck-consult: no session system prompt, no tool schemas.
func (a *Agent) Advise(ctx context.Context, question string) (string, error) {
	text, model, err := a.sideQuery(ctx, advisorSystem, question, strings.TrimSpace(a.AdvisorModel), 800)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[advisor %s]\n%s", model, text), nil
}

// sideQuery is a cheap extra completion against the recent transcript.
// It is the shared path for advisor and goal roles so we do not grow a
// second prompt stack. model empty uses the session model.
func (a *Agent) sideQuery(ctx context.Context, system, question, model string, maxTok int) (string, string, error) {
	s := a.Settings()
	if s.Provider == nil {
		return "", "", fmt.Errorf("no provider")
	}
	msgs := advisorMessages(a.MessagesSnapshot())
	msgs = append(msgs, llm.TextMessage(llm.RoleUser, question))
	if strings.TrimSpace(model) == "" {
		model = s.Model
	}
	prov := s.Provider
	if a.Route != nil && model != s.Model {
		p, resolved, err := a.Route(model)
		if err != nil {
			return "", "", err
		}
		if p != nil {
			prov = p
		}
		if resolved != "" {
			model = resolved
		}
	}
	a.debugf("side query %s via %s", model, prov.Name())
	if maxTok <= 0 {
		maxTok = 800
	}
	resp, err := prov.Stream(ctx, llm.Request{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: maxTok,
		Effort:    "low",
	}, nil)
	if err != nil {
		return "", model, err
	}
	if resp == nil {
		return "", model, fmt.Errorf("empty reply")
	}
	a.addUsage(resp.Usage)
	a.lastCallModel = model
	text := advisorReplyText(resp.Message)
	if text == "" {
		return "", model, fmt.Errorf("empty reply")
	}
	return text, model, nil
}

func advisorReplyText(msg llm.Message) string {
	if t := strings.TrimSpace(msg.Text()); t != "" {
		return t
	}
	// glm-5.3 and similar reasoning models put the answer in thinking
	// and leave content empty.
	var last string
	for _, b := range msg.Blocks {
		if b.Type == llm.BlockThinking {
			if t := strings.TrimSpace(b.Text); t != "" {
				last = t
			}
		}
	}
	return last
}

func advisorMessages(src []llm.Message) []llm.Message {
	const keep = 8
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
				if len(t) > 2000 {
					t = t[:2000]
				}
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
			case llm.BlockToolUse:
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "called " + b.Name})
			case llm.BlockToolResult:
				t := strings.TrimSpace(b.Text)
				if len(t) > 600 {
					t = t[:600]
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
