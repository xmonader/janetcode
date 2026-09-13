package tui

import (
	"context"
	"strings"
	"time"
)

// proofCommand implements /proof [commit]: the machine-readable evidence
// attached to a commit (see internal/proof) — which session, turn and
// stated intent wrote each of its files, and what actually demonstrated
// the work. Agent.Proof does the "show or attach" work; this only prints.
func (m *model) proofCommand(arg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	text, err := m.opts.Agent.Proof(ctx, m.opts.Cwd, strings.TrimSpace(arg))
	if err != nil {
		m.transcript.errorf("proof: %v", err)
		return
	}
	m.transcript.code(text)
}
