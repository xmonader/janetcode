package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/perm"
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
	if s.pending != nil {
		select {
		case s.pending.reply <- verdict{d: agent.Deny, reason: agent.CancelledReason}:
		default:
		}
		s.pending = nil
	}
	if s.asking != nil {
		select {
		case s.asking.reply <- nil:
		default:
		}
		s.asking = nil
	}
	s.mu.Unlock()
	s.emit(envelope{Type: "permission"}) // clear
	s.emit(envelope{Type: "question"})
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
	select {
	case p.reply <- verdict{d: d, reason: reason}:
	default:
	}
	s.emit(envelope{Type: "permission"})
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	reply := make(chan []agent.Answer, 1)
	s.mu.Lock()
	s.asking = &askReq{questions: qs, reply: reply}
	s.phase = "waiting for your answer"
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
	select {
	case req.reply <- body.Answers:
	default:
	}
	s.emit(envelope{Type: "question"})
	s.emitStatus()
	writeJSON(w, map[string]string{"ok": "1"})
}

func (s *server) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := s.ApproveWithReason(ctx, call)
	return d, err
}

func (s *server) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	reply := make(chan verdict, 1)
	s.mu.Lock()
	s.pending = &permReq{call: call, reply: reply}
	s.phase = "waiting for your approval"
	s.mu.Unlock()
	p := s.permOf(call)
	s.emit(envelope{Type: "permission", Perm: &p})
	s.emitStatus()
	select {
	case v := <-reply:
		return v.d, v.reason, nil
	case <-ctx.Done():
		s.mu.Lock()
		if s.pending != nil && s.pending.reply == reply {
			s.pending = nil
		}
		s.mu.Unlock()
		s.emit(envelope{Type: "permission"})
		return agent.Deny, "", ctx.Err()
	}
}

func (s *server) permOf(c agent.ToolCall) permPayload {
	rule := perm.Suggest(c.Name, perm.Subject(c.Name, c.Input)).String()
	return permPayload{
		ID:      c.ID,
		Name:    c.Name,
		Summary: summarizeInput(c.Name, string(c.Input), 80, s.opts.Cwd),
		Preview: c.Preview,
		Warning: c.Warning,
		Rule:    rule,
		Learned: c.Learned,
		Impact:  c.Impact,
	}
}
