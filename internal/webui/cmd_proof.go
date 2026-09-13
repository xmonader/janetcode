package webui

import (
	"context"
	"time"
)

// proofCommand implements /proof [commit]: the machine-readable evidence
// attached to a commit (see internal/proof) — which session, turn and
// stated intent wrote each of its files, and what actually demonstrated
// the work. Agent.Proof does the "show or attach" work; this only prints.
func (s *server) proofCommand(arg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	text, err := s.opts.Agent.Proof(ctx, s.opts.Cwd, arg)
	if err != nil {
		s.errf("proof: %v", err)
		return
	}
	s.sheet("proof", text)
}
