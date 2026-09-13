package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
)

// Proof is the "show or attach" logic behind /proof and `janetcode
// --proof`: it shows whatever machine-readable evidence (see
// internal/proof) is already attached to commit — which session, turn
// and stated intent wrote each of its files, and what actually
// demonstrated the work — attaching one from this session's own tracked
// changes first when the commit carries none yet. repoDir need not be
// the repository's top level; every path is normalised against it.
func (a *Agent) Proof(ctx context.Context, repoDir, commit string) (string, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		commit = "HEAD"
	}
	root, err := proof.Root(ctx, repoDir)
	if err != nil {
		return "", err
	}
	sha, err := proof.Resolve(ctx, root, commit)
	if err != nil {
		return "", fmt.Errorf("%s does not resolve to a commit: %w", commit, err)
	}
	if r, ok, rerr := proof.Read(ctx, root, sha); rerr != nil {
		return "", rerr
	} else if ok {
		return proof.Summary(r), nil
	}
	committed, err := proof.CommittedFiles(ctx, root, sha)
	if err != nil {
		return "", err
	}
	r := a.buildProof(root, committed)
	if r.Empty() {
		return "", fmt.Errorf("no proof for %s: this session made no tracked change to any file in that commit", proof.Short(sha))
	}
	if err := proof.Attach(ctx, root, sha, r); err != nil {
		return "", err
	}
	return proof.Summary(r), nil
}

// buildProof turns this session's tracked changes (see provenance.go) and
// their turns' verdicts (see verdict.go) into a proof.Record limited to
// committed — the plain proof.ChangeInput/TurnOutcomeInput types exist so
// that conversion happens here rather than proof importing this package
// back, which would cycle (agent already needs to call into proof).
func (a *Agent) buildProof(repoRoot string, committed []string) proof.Record {
	var sessionID string
	if sess, ok := a.Log.(*session.Session); ok {
		sessionID = sess.Meta.ID
	}
	changes := a.Changes()
	inputs := make([]proof.ChangeInput, 0, len(changes))
	turnsUsed := map[int]bool{}
	for _, c := range changes {
		if c.Undone {
			continue
		}
		add, del := c.Stats()
		inputs = append(inputs, proof.ChangeInput{
			Seq: c.Seq, Turn: c.Turn, Tool: c.Tool, Intent: c.Intent,
			Path: c.Path, Added: add, Removed: del,
		})
		turnsUsed[c.Turn] = true
	}
	var events []session.Event
	if a.Log != nil {
		events = a.Log.Events()
	}
	outcomes := make(map[int]proof.TurnOutcomeInput, len(turnsUsed))
	for tn := range turnsUsed {
		o := FoldOutcome(events, tn)
		outcomes[tn] = proof.TurnOutcomeInput{
			Verdict: string(o.Verdict), Why: o.Why, Checks: o.Checks,
			Passed: o.Passed, Failed: o.Failed, Tested: o.Tested, TestsEdited: o.TestsEdited,
		}
	}
	return proof.Build(sessionID, a.Model, repoRoot, inputs, outcomes, committed)
}

// gitCommitRe is the fast pre-check before the tokenising one: whether a
// bash command mentions "git" and "commit" at all, so the (slightly more
// expensive) statement-by-statement parse below only runs on the rare
// command that could match.
var gitCommitRe = regexp.MustCompile(`\bgit\b.*\bcommit\b`)

// statementSep splits a shell command on the separators between one
// statement and the next: && || ; | and newlines. It is a heuristic, not
// a shell parser — good enough to keep `git commit` in `cd x && git
// commit -am y` and `git commit; echo done` recognisable without one.
var statementSep = regexp.MustCompile(`&&|\|\||[;|\n]`)

// gitGlobalFlagsWithArg are git's global options that take a separate
// argument, so the subcommand token that follows can be found correctly:
// "git -C repo commit" must not mistake "repo" for the subcommand.
var gitGlobalFlagsWithArg = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true,
}

// looksLikeGitCommit reports whether cmd invokes `git commit` as an
// actual subcommand: `git -C x commit`, `git commit -am msg` and `git
// commit; other` match; `git commit-tree` (a different subcommand that
// happens to start with "commit") and `git log --grep commit` (the word
// "commit" as somebody else's argument) do not. It is a token scan, not a
// shell parse, so `echo git commit` also matches; that costs one wasted
// `git rev-parse HEAD` before and after the call, not a false attach,
// since checkCommitAttach only does anything when HEAD actually moved.
func looksLikeGitCommit(cmd string) bool {
	if !gitCommitRe.MatchString(cmd) {
		return false
	}
	for _, stmt := range statementSep.Split(cmd, -1) {
		fields := strings.Fields(stmt)
		for i := 0; i < len(fields); i++ {
			if fields[i] != "git" {
				continue
			}
			j := i + 1
			for j < len(fields) && strings.HasPrefix(fields[j], "-") {
				if gitGlobalFlagsWithArg[fields[j]] {
					j += 2
				} else {
					j++
				}
			}
			if j < len(fields) && fields[j] == "commit" {
				return true
			}
		}
	}
	return false
}

// checkCommitAttach runs after a bash call that looked like `git commit`
// succeeded: if HEAD actually moved, a proof record for this session's
// part of the new commit is built and attached (merging with any note
// another session already left there), and a notice tells the user where
// to look. preHead is what HEAD was before the call ran ("" when it could
// not be read, e.g. an unborn branch); it costs one more `git rev-parse`
// only for commands that matched looksLikeGitCommit in the first place.
func (a *Agent) checkCommitAttach(ctx context.Context, preHead string, emit func(Event)) {
	if a.Snapshots == nil || !a.ProofNotes {
		return
	}
	root := a.Snapshots.Root
	postHead, err := proof.Resolve(ctx, root, "HEAD")
	if err != nil || postHead == "" || postHead == preHead {
		return
	}
	committed, err := proof.CommittedFiles(ctx, root, postHead)
	if err != nil {
		return
	}
	r := a.buildProof(root, committed)
	if r.Empty() {
		// Not an error: the commit may be entirely someone else's changes,
		// a merge, or files this session never touched with the tracked
		// tools. Nothing to say.
		return
	}
	if err := proof.Attach(ctx, root, postHead, r); err != nil {
		emit(Event{Kind: EvNotice, Text: "proof: could not attach evidence to " + proof.Short(postHead) + ": " + err.Error()})
		return
	}
	emit(Event{Kind: EvNotice, Text: "proof attached to " + proof.Short(postHead) + " · /proof shows it"})
}

// headForCommitCheck reads HEAD cheaply, only ever called for a bash
// command that already matched looksLikeGitCommit.
func (a *Agent) headForCommitCheck(ctx context.Context) string {
	if a.Snapshots == nil {
		return ""
	}
	head, _ := proof.Resolve(ctx, a.Snapshots.Root, "HEAD")
	return head
}
