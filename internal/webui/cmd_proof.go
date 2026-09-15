package webui

import (
	"github.com/xmonader/janetcode/internal/proof"
)

// proofCommand implements /proof [commit | push [remote] | fetch [remote]]
// through proof.Exec (see there): show the proof note on a commit,
// attaching one from this session first if there is none yet, or move
// proof notes to and from a remote.
func (s *server) proofCommand(arg string) {
	s.showResult(proof.Exec(s.life, s.opts.Cwd, proof.ParseArgs(arg), s.opts.Agent.AttachProof))
}
