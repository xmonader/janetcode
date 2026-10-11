package webui

import (
	"github.com/xmonader/janetcode/internal/proof"
)

// proofCommand implements /proof [commit | push [remote] | fetch [remote] |
// verify [commit] | key] through proof.Exec (see there): show the proof
// note on a commit, attaching one from this session first if there is
// none yet, move proof notes to and from a remote, or check a commit's
// signed evidence. verify is static here (signature,
// subject, which claims have a record): re-running the recorded commands
// takes as long as they do, which `janetcode --proof verify` is for.
func (s *server) proofCommand(arg string) {
	a := proof.ParseArgs(arg)
	a.Static = true
	s.showResult(proof.Exec(s.life, s.cwd(), a, s.opts.Agent.AttachProof))
}
