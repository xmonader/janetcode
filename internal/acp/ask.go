package acp

import (
	"context"
	"fmt"

	"github.com/xmonader/janetcode/internal/agent"
)

// Ask puts questions to the editor as permission prompts, one per
// question, with each option a button: the ACP protocol has no
// free-text prompt, so a question is answered by choosing, never by
// typing. MCP elicitation uses it (mcptool.ElicitViaAsker), where a
// text field is therefore limited to its default or "leave empty", and a
// dismissed prompt reads as "skipped" (a cancel).
//
// The question goes to the session that is mid-turn, since an MCP server
// asks while one of its tools is running; with none busy, to the newest.
func (s *Server) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	as := s.askSession()
	if as == nil {
		return nil, fmt.Errorf("acp: no session to ask")
	}
	out := make([]agent.Answer, 0, len(qs))
	for qi, q := range qs {
		title := q.Prompt
		if q.Detail != "" {
			title += "\n" + q.Detail
		}
		params := requestPermissionParams{
			SessionID: as.id,
			ToolCall:  toolUpdate{SessionUpdate: "tool_call", ToolCallID: fmt.Sprintf("ask-%d", qi), Title: title, Kind: "other", Status: statusPending},
		}
		for i, o := range q.Options {
			kind := optAllowOnce
			if o.Label == "Decline request" || o.Label == "Cancel request" {
				kind = optRejectOnce
			}
			params.Options = append(params.Options, permissionOption{OptionID: fmt.Sprintf("opt-%d", i), Name: o.Label, Kind: kind})
		}
		if len(params.Options) == 0 {
			params.Options = []permissionOption{{OptionID: optRejectOnce, Name: "Cancel", Kind: optRejectOnce}}
		}
		var res requestPermissionResult
		if err := s.c.call(ctx, "session/request_permission", params, &res); err != nil {
			return nil, err
		}
		a := agent.Answer{Label: "skipped"}
		if res.Outcome.Outcome != "cancelled" {
			for i, o := range q.Options {
				if res.Outcome.OptionID == fmt.Sprintf("opt-%d", i) {
					a = agent.Answer{Label: o.Label}
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// askSession is the session a server-initiated question belongs to.
func (s *Server) askSession() *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *acpSession
	for _, as := range s.sessions {
		if len(as.turn) > 0 {
			return as
		}
		if newest == nil || as.seq > newest.seq {
			newest = as
		}
	}
	return newest
}
