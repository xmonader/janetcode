package agent

import (
	"context"

	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
)

// attachEvidence signs and attaches the evidence statement (see
// internal/proof's evidence.go) for the session's part of commit sha,
// beside the proof note r that was just attached. It does nothing when
// the session has no signer (a test, an SDK embedder that did not ask for
// one) or the "evidence" behaviour is off. The statement is built from
// this session's own record r — before any merge with another session's
// note — and the log; the merge with other sessions' evidence happens in
// proof.AttachEvidence.
func (a *Agent) attachEvidence(ctx context.Context, root, sha string, r proof.Record, versions map[string]proof.FileVersions) error {
	if a.Evidence == nil || a.Off["evidence"] || r.Empty() {
		return nil
	}
	tree, err := proof.TreeOf(ctx, root, sha)
	if err != nil {
		return err
	}
	var events []session.Event
	if a.Log != nil {
		events = a.logEvents()
	}
	claims := map[int]string{}
	for _, t := range r.Turns {
		if c := FoldOutcomeIn(events, t.Turn, root).Claimed; c != "" {
			claims[t.Turn] = c
		}
	}
	work := a.RootDir()
	if work == "" {
		work = root
	}
	in := proof.EvidenceInput{
		Session: r.Session, Model: r.Model, SystemPrompt: a.SystemPrompt(),
		RepoRoot: root, WorkRoot: work, Events: events,
		Sandbox: a.sandboxEvidence(), Claims: claims,
	}
	_, err = proof.AttachEvidence(ctx, root, sha, proof.BuildEvidence(sha, tree, r, versions, in), a.Evidence)
	return err
}

// sandboxEvidence is the sandbox the session's shell runs under now: the
// profile model-run checks are recorded (and re-run) under.
func (a *Agent) sandboxEvidence() proof.SandboxEvidence {
	if a.Sandbox == nil || !a.Sandbox.Enabled() {
		return proof.SandboxEvidence{Profile: proof.NoSandbox}
	}
	backend := a.Sandbox.Backend()
	return proof.SandboxEvidence{
		Profile: a.Sandbox.ProfileName(), Backend: backend,
		SHA256: proof.SandboxDigest(a.Sandbox.Profile(), backend),
	}
}
