package agent

// portLoop is the per-turn state of the loop behaviours ported from
// hero_agent (plan carry, read dedup, edge-rules note, step-limit closing
// reply, adaptive risk). It is reset at the start of every turn.
type portLoop struct {
	planCarried bool
	// reads outlives the turn: whether an earlier copy is live is judged
	// against the history, not remembered.
	reads readViews
	// overflow: a compaction forced by a context-overflow error, which
	// must not be skipped. skipSaid: the skipped-summary notice was given.
	overflow, skipSaid bool
}

func (p *portLoop) startTurn() { *p = portLoop{reads: p.reads} }

// riskyTurn gates the expensive checks; see risk.go. Until that lands every
// turn counts as risky, i.e. all checks on.
func (a *Agent) riskyTurn() bool { return true }
