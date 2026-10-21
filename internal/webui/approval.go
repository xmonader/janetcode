package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/shell"
)

type askReq struct {
	questions []agent.Question
	reply     chan []agent.Answer
}

type permReq struct {
	call  agent.ToolCall
	reply chan verdict
}

type verdict struct {
	d      agent.Decision
	reason string
	// rule is which rung of the call's ladder an "always" answer chose,
	// "" for the one the card offered first (see agent.ToolCall.Rungs).
	rule string
}

func (s *server) doCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	p, a := s.pending, s.asking
	s.pending, s.asking = nil, nil
	s.mu.Unlock()
	// The page is told to clear before the callers are answered: a prompt
	// waiting behind this one (a subagent outside the turn) can come up
	// the moment its caller is, and a clear sent after it took its card
	// down with nothing left on the page to answer it.
	s.emit(envelope{Type: "permission"}) // clear
	s.emit(envelope{Type: "question"})
	if p != nil {
		select {
		case p.reply <- verdict{d: agent.Deny, reason: agent.CancelledReason}:
		default:
		}
	}
	if a != nil {
		select {
		case a.reply <- nil:
		default:
		}
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) permission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		// Rule is the rung of the prompt's ladder to save; ignored
		// unless it is one of them (see agent.ToolCall.Rungs).
		Rule string `json:"rule"`
		// ID is the call the page is answering. When it is given and is
		// not the call pending now, the answer is refused rather than
		// applied to a prompt the user has not seen.
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var d agent.Decision
	switch body.Decision {
	case "allow":
		d = agent.Allow
	case "session":
		d = agent.AllowSession
	case "always":
		d = agent.AllowAlways
	case "deny":
		d = agent.Deny
	case "cancel":
		d = agent.Deny
	default:
		http.Error(w, "unknown decision", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	p := s.pending
	if p != nil && body.ID != "" && body.ID != p.call.ID {
		s.mu.Unlock()
		http.Error(w, "that prompt is no longer the one waiting", http.StatusConflict)
		return
	}
	s.pending = nil
	s.mu.Unlock()
	if p == nil {
		http.Error(w, "nothing to answer", http.StatusConflict)
		return
	}
	reason := body.Reason
	if body.Decision == "cancel" {
		reason = agent.CancelledReason
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
	}
	// Cleared before the caller is answered: answering lets the prompt
	// waiting behind this one up, and a clear sent after its card took
	// that card down (see doCancel).
	s.emit(envelope{Type: "permission"})
	// The card shows an edit's diff whole (see permOf), so approving one
	// that carried it is approving lines that were in front of the
	// reader: not comprehension debt (see package tour).
	if d != agent.Deny && p.call.PreviewShown() && s.opts.Agent != nil {
		s.opts.Agent.MarkReviewed(p.call.ID)
	}
	select {
	case p.reply <- verdict{d: d, reason: reason, rule: body.Rule}:
	default:
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

// Ask shows one batch of questions at a time, as ApproveWithRule shows one
// prompt: the ask tool is not mutating, so two calls in one reply run in
// parallel, and the second used to overwrite the first, whose caller then
// waited on a reply nothing could send.
func (s *server) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	select {
	case s.askSlot <- struct{}{}:
		defer func() { <-s.askSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	reply := make(chan []agent.Answer, 1)
	s.mu.Lock()
	// A copy of its own: AskTimedOut marks these proceeded in place, and
	// qs is the caller's and rides the event below to every page.
	s.asking = &askReq{questions: append([]agent.Question(nil), qs...), reply: reply}
	s.setPhaseLocked("waiting for your answer")
	s.mu.Unlock()
	s.emit(envelope{Type: "question", Questions: qs})
	s.emitStatus()
	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		s.mu.Lock()
		if s.asking != nil && s.asking.reply == reply {
			s.asking = nil
		}
		s.mu.Unlock()
		s.emit(envelope{Type: "question"})
		return nil, ctx.Err()
	}
}

// AskTimedOut is agent.AskTimeoutNotifier: the turn went on with the
// recommendation, so the open question stops saying it holds the turn and
// the page shows it as one that takes a late answer.
func (s *server) AskTimedOut(qs []agent.Question, _ []agent.Answer) {
	s.mu.Lock()
	if s.asking == nil {
		s.mu.Unlock()
		return
	}
	for i := range s.asking.questions {
		s.asking.questions[i].Proceeded = true
	}
	open := append([]agent.Question(nil), s.asking.questions...)
	s.setPhaseLocked("")
	s.mu.Unlock()
	s.emit(envelope{Type: "question", Questions: open})
	s.emitStatus()
}

func (s *server) questionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Answers []agent.Answer `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	req := s.asking
	s.asking = nil
	s.mu.Unlock()
	if req == nil {
		http.Error(w, "no question pending", http.StatusConflict)
		return
	}
	s.emit(envelope{Type: "question"}) // before the answer: see permission
	select {
	case req.reply <- body.Answers:
	default:
	}
	s.emitStatus()
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := s.ApproveWithReason(ctx, call)
	return d, err
}

func (s *server) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	d, reason, _, err := s.ApproveWithRule(ctx, call)
	return d, reason, err
}

// ApproveWithRule shows one prompt at a time. Subagents that bubble their
// confirmations to the user run in parallel and can ask at once; the page
// has one prompt slot, and a second call used to overwrite the first,
// whose caller then waited on a reply nothing could send. The others wait
// their turn here.
func (s *server) ApproveWithRule(ctx context.Context, call agent.ToolCall) (agent.Decision, string, string, error) {
	select {
	case s.approveSlot <- struct{}{}:
		defer func() { <-s.approveSlot }()
	case <-ctx.Done():
		return agent.Deny, "", "", ctx.Err()
	}
	reply := make(chan verdict, 1)
	s.mu.Lock()
	s.pending = &permReq{call: call, reply: reply}
	s.setPhaseLocked("waiting for your approval")
	s.mu.Unlock()
	p := s.permOf(call)
	s.emit(envelope{Type: "permission", Perm: &p})
	s.emitStatus()
	select {
	case v := <-reply:
		return v.d, v.reason, v.rule, nil
	case <-ctx.Done():
		s.mu.Lock()
		if s.pending != nil && s.pending.reply == reply {
			s.pending = nil
		}
		s.mu.Unlock()
		s.emit(envelope{Type: "permission"})
		return agent.Deny, "", "", ctx.Err()
	}
}

func (s *server) permOf(c agent.ToolCall) permPayload {
	rule := c.Rule
	var rungs []permRung
	if len(c.Rungs) > 1 {
		for _, r := range c.Rungs {
			rungs = append(rungs, permRung{Rule: r.Rule, Learned: r.Learned})
		}
	}
	preview := c.Preview
	// A command is what the reader is approving: when the one-line summary
	// cannot hold it all, the card shows it whole.
	if preview == "" && c.Name == "bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(c.Input, &in) == nil {
			cmd := shell.TrimWorkspaceCd(in.Command, s.cwd())
			if len([]rune(cmd)) > 80 || strings.Contains(cmd, "\n") {
				preview = cmd
			}
		}
	}
	return permPayload{
		ID:        c.ID,
		Name:      c.Name,
		Summary:   present.ToolSummary(c.Name, string(c.Input), 80, s.cwd()),
		Preview:   preview,
		Warning:   c.Warning,
		Rule:      rule,
		Rungs:     rungs,
		RuleIndex: c.RuleIndex,
		Learned:   c.Learned,
		Impact:    c.Impact,
	}
}
