package webui

import "github.com/xmonader/janetcode/internal/agent"

// accept runs /accept through agent.AcceptCommand: starts test-writing
// mode (starting the request as a turn), reports status, or resolves
// approve/lock/unlock/command/off. agent.ParseAcceptArgs is the one
// parser both front-ends use, so a spelling accepted here means the same
// thing in the terminal.
func (s *server) accept(arg string) {
	res, err := s.opts.Agent.AcceptCommand(agent.ParseAcceptArgs(arg), s.opts.Cwd)
	s.showResult(res, err)
	if err == nil && res.Handover != "" {
		s.startTurn(res.Handover)
	}
}
