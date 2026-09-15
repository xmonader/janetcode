package tui

import (
	"github.com/xmonader/janetcode/internal/proof"
)

// proofCommand implements /proof [commit | push [remote] | fetch [remote]]
// through proof.Exec (see there): show the proof note on a commit,
// attaching one from this session first if there is none yet, or move
// proof notes to and from a remote.
func (m *model) proofCommand(arg string) {
	m.showResult(proof.Exec(m.life, m.opts.Cwd, proof.ParseArgs(arg), m.opts.Agent.AttachProof))
}
