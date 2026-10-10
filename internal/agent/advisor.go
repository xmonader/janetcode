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
	text, model, err := a.sideQuery(ctx, advisorSystem, question, a.AdvisorModelName(), 800)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[advisor %s]\n%s", model, text), nil
}

// sideQuery is a cheap extra completion against the recent transcript.
// It is the shared path for advisor and goal roles so we do not grow a
// second prompt stack. model empty uses the session model.
func (a *Agent) sideQuery(ctx context.Context, system, question, model string, maxTok int) (string, string, error) {
	r, err := a.sideQueryReply(ctx, system, question, model, maxTok)
	return r.Text, r.Model, err
}

// sideReply is a side query's answer with what a caller needs to judge
// it: whether the provider stopped it at the token limit, and whether the
// text came only from thinking because the reply itself was empty.
type sideReply struct {
	Text, Model  string
	Truncated    bool
	ThinkingOnly bool
}

// sideQueryReply is sideQuery reporting how the reply ended.
func (a *Agent) sideQueryReply(ctx context.Context, system, question, model string, maxTok int) (sideReply, error) {
	msgs := sideTranscript(a.MessagesSnapshot(), 8, 2000, 600)
	msgs = append(msgs, llm.TextMessage(llm.RoleUser, question))
	return a.sideCall(ctx, system, msgs, model, maxTok)
}

// sideCall is one completion with no tools on model (empty: the session's
// own), routed to the provider that serves it. msgs is all it sees: a
// caller that must not show the transcript (blindtest.go) passes its own.
func (a *Agent) sideCall(ctx context.Context, system string, msgs []llm.Message, model string, maxTok int) (sideReply, error) {
	return a.sideCallEffort(ctx, system, msgs, model, maxTok, "")
}

// sideCallEffort is sideCall with a reasoning_effort for providers that
// send one only on request (llm.Request.ReasoningEffort).
func (a *Agent) sideCallEffort(ctx context.Context, system string, msgs []llm.Message, model string, maxTok int, wireEffort string) (sideReply, error) {
	s := a.Settings()
	if s.Provider == nil {
		return sideReply{}, fmt.Errorf("no provider")
	}
	if strings.TrimSpace(model) == "" {
		model = s.Model
	}
	prov := s.Provider
	if a.Route != nil && model != s.Model {
		p, resolved, err := a.Route(model)
		if err != nil {
			return sideReply{}, err
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

		ReasoningEffort: wireEffort,
	}, nil)
	if err != nil {
		return sideReply{Model: model}, err
	}
	if resp == nil {
		return sideReply{Model: model}, fmt.Errorf("empty reply")
	}
	a.addUsage(model, resp.Usage)
	// lastCallModel is which model answered the *turn's* call, for the
	// assistant event logged next to it. A side query is not that call,
	// and the advisor tool runs in the parallel half of the tool loop, so
	// writing it here both mislabelled the event and raced the turn
	// goroutine that reads it.
	text := advisorReplyText(resp.Message)
	if text == "" {
		return sideReply{Model: model}, fmt.Errorf("empty reply")
	}
	return sideReply{
		Text:         text,
		Model:        model,
		Truncated:    resp.StopReason == llm.StopMaxTokens,
		ThinkingOnly: strings.TrimSpace(resp.Message.Text()) == "",
	}, nil
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

// sideTranscript flattens the tail of a conversation into plain text
// messages for a side query. Tool calls and results become text, so the
// request needs no tool schemas and nothing has to be paired.
//
// The result always starts on a user message: taking the last `keep`
// messages lands on an assistant turn half the time, and a request whose
// first message is from the assistant is refused outright by Anthropic.
func sideTranscript(src []llm.Message, keep, textMax, resultMax int) []llm.Message {
	if keep <= 0 {
		keep = 8
	}
	if len(src) > keep {
		src = src[len(src)-keep:]
	}
	for len(src) > 0 && src[0].Role != llm.RoleUser {
		src = src[1:]
	}
	out := make([]llm.Message, 0, len(src))
	for _, m := range src {
		var blocks []llm.Block
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockThinking:
				continue
			case llm.BlockText:
				t := prefix(b.Text, orAll(textMax, len(b.Text)))
				if t == "" {
					continue
				}
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: t})
			case llm.BlockToolUse:
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "called " + b.Name})
			case llm.BlockToolResult:
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: prefix(b.Text, orAll(resultMax, len(b.Text)))})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, llm.Message{Role: m.Role, Blocks: blocks})
	}
	return out
}

// orAll is max when it bounds anything, else all: a cap of zero or less
// means "no cap".
func orAll(max, all int) int {
	if max > 0 {
		return max
	}
	return all
}
