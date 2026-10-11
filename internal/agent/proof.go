package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
)

// Proof is the "show or attach" logic behind /proof: it shows whatever
// machine-readable evidence (see internal/proof) is already attached to
// commit — which session, turn and stated intent wrote each of its files,
// and what actually demonstrated the work — attaching one from this
// session's own tracked changes first (see AttachProof) when the commit
// carries none yet. repoDir need not be the repository's top level; every
// path is normalised against it.
func (a *Agent) Proof(ctx context.Context, repoDir, commit string) (string, error) {
	root, err := proof.Root(ctx, repoDir)
	if err != nil {
		return "", err
	}
	text, _, err := proof.Show(ctx, root, commit, a.AttachProof)
	return text, err
}

// AttachProof builds a proof note for commit sha from this session's
// tracked changes and attaches it, returning its summary; an error when
// this session changed none of the commit's files. It is the
// proof.AttachFunc a live session passes to proof.Exec.
func (a *Agent) AttachProof(ctx context.Context, root, sha string) (string, error) {
	if a.Off["proof"] {
		return "", nil
	}
	committed, err := proof.CommittedFiles(ctx, root, sha)
	if err != nil {
		return "", err
	}
	r, versions, err := a.buildProof(ctx, root, sha, committed)
	if err != nil {
		return "", err
	}
	if r.Empty() {
		return "", fmt.Errorf("no proof for %s: this session made no tracked change to any file in that commit", proof.Short(sha))
	}
	if err := proof.Attach(ctx, root, sha, r); err != nil {
		return "", err
	}
	sum := proof.Summary(r)
	if err := a.attachEvidence(ctx, root, sha, r, versions); err != nil {
		sum += "\nevidence: not signed: " + err.Error()
	}
	return sum, nil
}

// buildProof turns this session's tracked changes (see provenance.go) and
// their turns' verdicts (see verdict.go) into a proof.Record limited to
// committed — the plain proof.ChangeInput/TurnOutcomeInput types exist so
// that conversion happens here rather than proof importing this package
// back, which would cycle (agent already needs to call into proof). The
// git reads Build needs to tell a change that actually landed in commit
// from one that only shares its path with an earlier commit (see
// proof.changeLandedIn) happen here too, via proof.CommittedFileVersions,
// keeping every git call behind the proof package as everywhere else in
// this file. The versions are returned too, for the signed evidence
// built beside the note (see evidence.go).
func (a *Agent) buildProof(ctx context.Context, repoRoot, commit string, committed []string) (proof.Record, map[string]proof.FileVersions, error) {
	versions, err := proof.CommittedFileVersions(ctx, repoRoot, commit, committed)
	if err != nil {
		return proof.Record{}, nil, err
	}
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
			Before: c.Before, After: c.After,
		})
		turnsUsed[c.Turn] = true
	}
	var events []session.Event
	if a.Log != nil {
		events = a.Log.Events()
	}
	outcomes := make(map[int]proof.TurnOutcomeInput, len(turnsUsed))
	for tn := range turnsUsed {
		o := FoldOutcomeIn(events, tn, repoRoot)
		outcomes[tn] = proof.TurnOutcomeInput{
			Verdict: string(o.Verdict), Why: o.Why, Checks: o.Checks,
			Passed: o.Passed, Failed: o.Failed, Tested: o.Tested, TestsEdited: o.TestsEdited,
			Mutation:  foldMutation(events, tn),
			Integrity: foldLedger(events, tn),
			Conflict:  foldConflict(events, tn),
		}
	}
	return proof.Build(sessionID, a.Settings().Model, repoRoot, inputs, outcomes, versions), versions, nil
}

// mutationExtra mirrors the Extra JSON internal/mutate's Report.Event
// writes onto a session.TypeMutation event; duplicated here (rather than
// imported) because internal/mutate does not export it and pulling in the
// whole package just for this shape is not worth a new dependency for a
// four-field struct.
type mutationExtra struct {
	Killed   int    `json:"killed"`
	Survived int    `json:"survived"`
	Total    int    `json:"total"`
	Command  string `json:"command"`
}

// foldMutation returns the turn's mutation-testing score, from the latest
// session.TypeMutation event logged for it, or nil when the turn logged
// none.
func foldMutation(events []session.Event, turn int) *proof.Mutation {
	var m *proof.Mutation
	for _, e := range events {
		if e.Type != session.TypeMutation || e.Turn != turn {
			continue
		}
		var x mutationExtra
		if json.Unmarshal(e.Extra, &x) != nil {
			continue
		}
		m = &proof.Mutation{Killed: x.Killed, Survived: x.Survived, Total: x.Total}
	}
	return m
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
	if a.Off["proof"] || a.userRepo() == nil || !a.Settings().ProofNotes {
		return
	}
	root := a.userRepo().Root
	postHead, err := proof.Resolve(ctx, root, "HEAD")
	if err != nil || postHead == "" || postHead == preHead {
		return
	}
	if a.attachCommitProof(ctx, postHead, emit) {
		a.proofCommits = append(a.proofCommits, postHead)
		emit(Event{Kind: EvNotice, Text: ProofAttachedNotice + proof.Short(postHead) + " · /proof shows it"})
	}
}

// attachCommitProof builds and attaches the proof note for sha from this
// session's changes, reporting whether one was attached. A failure is told
// to the user; a commit carrying none of this session's changes is not an
// error — it may be someone else's work, a merge, or files never touched
// with the tracked tools — and attaches nothing.
func (a *Agent) attachCommitProof(ctx context.Context, sha string, emit func(Event)) bool {
	root := a.Snapshots.Root
	committed, err := proof.CommittedFiles(ctx, root, sha)
	if err != nil {
		return false
	}
	r, versions, err := a.buildProof(ctx, root, sha, committed)
	if err != nil {
		emit(Event{Kind: EvNotice, Text: "proof: could not read " + proof.Short(sha) + "'s content to attach evidence: " + err.Error()})
		return false
	}
	if r.Empty() {
		return false
	}
	if err := proof.Attach(ctx, root, sha, r); err != nil {
		emit(Event{Kind: EvNotice, Text: "proof: could not attach evidence to " + proof.Short(sha) + ": " + err.Error()})
		return false
	}
	if err := a.attachEvidence(ctx, root, sha, r, versions); err != nil {
		emit(Event{Kind: EvNotice, Text: "proof: could not sign evidence for " + proof.Short(sha) + ": " + err.Error()})
	}
	return true
}

// refreshProofNotes re-attaches the notes this turn put on its own commits
// once the turn is over: a commit made mid-turn was noted before the
// turn-end gates ran, and their runs belong in its evidence. Attach merges
// by session and turn, so the refreshed turn replaces the earlier one.
func (a *Agent) refreshProofNotes(ctx context.Context, emit func(Event)) {
	if a.userRepo() == nil {
		return
	}
	for _, sha := range a.proofCommits {
		a.attachCommitProof(ctx, sha, emit)
	}
	a.proofCommits = nil
}

// ProofAttachedNotice begins the notice emitted when the session's own
// commit has had a proof note attached, so a front-end can recognise the
// moment without matching prose that may be reworded.
const ProofAttachedNotice = "proof attached to "

// headForCommitCheck reads HEAD cheaply, only ever called for a bash
// command that already matched looksLikeGitCommit.
func (a *Agent) headForCommitCheck(ctx context.Context) string {
	if a.userRepo() == nil {
		return ""
	}
	head, _ := proof.Resolve(ctx, a.userRepo().Root, "HEAD")
	return head
}
