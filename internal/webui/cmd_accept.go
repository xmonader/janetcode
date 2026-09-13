package webui

import "github.com/xmonader/janetcode/internal/agent"

// accept runs /accept: starts test-writing mode, reports status, or
// resolves approve/lock/unlock/off. agent.ParseAcceptArgs is the one
// parser both front-ends use, so a spelling accepted here means the same
// thing in the terminal.
func (s *server) accept(arg string) {
	ag := s.opts.Agent
	a := agent.ParseAcceptArgs(arg)
	switch {
	case a.Status:
		s.sheet("accept", ag.AcceptStatus())
	case a.Approve:
		cmd, err := ag.AcceptApprove()
		if err != nil {
			s.errf("accept: %v", err)
			return
		}
		s.notice("accept: locked · verifies with: " + cmd)
	case a.Lock:
		if err := ag.AcceptLock(); err != nil {
			s.errf("accept: %v", err)
			return
		}
		s.notice("accept: locked")
	case a.Unlock:
		if err := ag.AcceptUnlock(); err != nil {
			s.errf("accept: %v", err)
			return
		}
		s.notice("accept: unlocked · edits to the test files are allowed until /accept lock")
	case a.Off:
		ag.AcceptOff()
		s.notice("accept: off")
	default:
		ag.AcceptStart(a.Request, s.opts.Cwd)
		s.startTurn(a.Request)
	}
}
