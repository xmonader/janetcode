package tui

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/proof"
)

// proofCommand implements /proof [commit | push [remote] | fetch [remote]].
// With a commit (default HEAD) it shows the machine-readable evidence
// attached to it (see internal/proof) -- which session, turn and stated
// intent wrote each of its files, and what actually demonstrated the work
// -- attaching one from this session first if there is none yet;
// Agent.Proof does that "show or attach" work, this only prints. push and
// fetch move proof notes to and from a remote (see internal/proof.Push,
// Fetch, ConfigureFetch): notes are local only until pushed, so without
// this a note attached in one clone never reaches anyone else's.
func (m *model) proofCommand(arg string) {
	a := proof.ParseArgs(arg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch a.Action {
	case proof.ActionPush:
		if err := proof.Push(ctx, m.opts.Cwd, a.Remote); err != nil {
			m.transcript.errorf("proof push: %v", err)
			return
		}
		m.note("pushed proof notes to " + a.Remote)
	case proof.ActionFetch:
		if err := proof.Fetch(ctx, m.opts.Cwd, a.Remote); err != nil {
			m.transcript.errorf("proof fetch: %v", err)
			return
		}
		if err := proof.ConfigureFetch(ctx, m.opts.Cwd, a.Remote); err != nil {
			m.transcript.errorf("proof fetch: %v", err)
			return
		}
		m.note("fetched proof notes from " + a.Remote + " · future fetches from it will bring them too")
	default:
		text, err := m.opts.Agent.Proof(ctx, m.opts.Cwd, a.Commit)
		if err != nil {
			m.transcript.errorf("proof: %v", err)
			return
		}
		m.transcript.code(text)
	}
}
