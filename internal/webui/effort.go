package webui

// effort implements /effort [tier|up|down] through the agent's core
// command (see agent.EffortCommand): this front-end only passes the
// argument on and renders what came back, so the wording and the ladder
// are the terminal's.
func (s *server) effort(arg string) {
	if s.opts.Agent == nil {
		s.errf("no agent")
		return
	}
	s.showResult(s.opts.Agent.EffortCommand(arg))
	s.emitStatus()
}
