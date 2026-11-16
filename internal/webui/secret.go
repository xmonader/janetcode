package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/xmonader/janetcode/internal/agent"
)

// secretPayload is what the page is told about a secret request: the
// model's words, never a value. It rides the event log, which is replayed
// to every page that connects, so nothing secret may ever be in it.
type secretPayload struct {
	Name    string `json:"name"`
	EnvVar  string `json:"env_var"`
	Purpose string `json:"purpose"`
	Usage   string `json:"usage"`
}

type secretReq struct {
	req   agent.SecretRequest
	reply chan agent.SecretReply
}

func payloadOf(r agent.SecretRequest) *secretPayload {
	return &secretPayload{Name: r.Name, EnvVar: r.EnvVar, Purpose: r.Purpose, Usage: r.Usage}
}

// AskSecret implements agent.SecretAsker: one card at a time, a second
// request waits its turn, and the value comes back only through
// secretAPI's POST body to the caller's channel.
func (s *server) AskSecret(ctx context.Context, req agent.SecretRequest) (agent.SecretReply, error) {
	select {
	case s.secretSlot <- struct{}{}:
		defer func() { <-s.secretSlot }()
	case <-ctx.Done():
		return agent.SecretReply{}, ctx.Err()
	}
	reply := make(chan agent.SecretReply, 1)
	s.mu.Lock()
	s.secreting = &secretReq{req: req, reply: reply}
	s.setPhaseLocked("waiting for your secret")
	s.mu.Unlock()
	s.emit(envelope{Type: "secret", Secret: payloadOf(req)})
	s.emitStatus()
	select {
	case v := <-reply:
		return v, nil
	case <-ctx.Done():
		s.mu.Lock()
		if s.secreting != nil && s.secreting.reply == reply {
			s.secreting = nil
		}
		s.mu.Unlock()
		s.emit(envelope{Type: "secret"})
		return agent.SecretReply{}, ctx.Err()
	}
}

// secretAPI takes the user's answer: {"value": "..."} or {"declined": true}.
// The body is read once into the reply channel; it is not logged, echoed
// or kept.
func (s *server) secretAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Value    string `json:"value"`
		Declined bool   `json:"declined"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	req := s.secreting
	s.secreting = nil
	s.mu.Unlock()
	if req == nil {
		http.Error(w, "no secret requested", http.StatusConflict)
		return
	}
	s.emit(envelope{Type: "secret"}) // cleared before the caller is answered
	select {
	case req.reply <- agent.SecretReply{Value: body.Value, Declined: body.Declined || body.Value == ""}:
	default:
	}
	s.emitStatus()
	writeJSON(w, map[string]string{"ok": "1"})
}

// secretPending is the open request as the page shows it, nil when none.
func (s *server) secretPending() *secretPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secreting == nil {
		return nil
	}
	return payloadOf(s.secreting.req)
}
