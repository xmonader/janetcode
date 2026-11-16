package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestLooksLikeGitCommit(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"git commit -am 'msg'", true},
		{"git -C repo commit -m x", true},
		{"cd repo && git commit -m x", true},
		{"git add -A && git commit -m x", true},
		{"git commit; echo done", true},
		{"git commit-tree -m x HEAD^{tree}", false},
		{"git log --grep commit", false},
		{"git status", false},
	}
	for _, c := range cases {
		if got := looksLikeGitCommit(c.cmd); got != c.want {
			t.Errorf("looksLikeGitCommit(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// TestAutomaticProofAttachOnCommit is the end-to-end path: a turn writes a
// file and then commits it with a bash call, and the harness attaches
// proof of that turn's own work to the new commit without being asked.
func TestAutomaticProofAttachOnCommit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		toolCall("t2", "bash", `{"command":"git add -A && git commit -m 'add foo'"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.ProofNotes = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess

	evs, emit := collect()
	if err := a.Run(context.Background(), "add foo", emit); err != nil {
		t.Fatal(err)
	}
	var sawNotice bool
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "proof attached to") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatalf("expected a proof-attached notice; events: %+v", *evs)
	}
	sha := headOf(t, root)
	r, ok, err := proof.Read(context.Background(), root, sha)
	if err != nil || !ok {
		t.Fatalf("proof.Read: ok=%v err=%v", ok, err)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "foo.go" {
		t.Fatalf("files = %+v", r.Files)
	}
	if r.Session != sess.Meta.ID {
		t.Fatalf("session = %q, want %q", r.Session, sess.Meta.ID)
	}
}

// TestProofNotesGateSkipsAutomaticAttach checks that ProofNotes: false
// stops the automatic attach without failing the commit itself.
func TestProofNotesGateSkipsAutomaticAttach(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		toolCall("t2", "bash", `{"command":"git add -A && git commit -m 'add foo'"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.ProofNotes = false
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess

	evs, emit := collect()
	if err := a.Run(context.Background(), "add foo", emit); err != nil {
		t.Fatal(err)
	}
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "proof attached") {
			t.Fatalf("proof_notes disabled but got a proof notice: %s", e.Text)
		}
	}
	sha := headOf(t, root)
	if _, ok, _ := proof.Read(context.Background(), root, sha); ok {
		t.Fatal("expected no proof note when ProofNotes is false")
	}
}

// TestAgentProofAttachesFromSlashCommand covers the /proof "show or
// attach" path directly: the session made the change but never ran a
// commit itself (as if the user committed it in another terminal), and
// Agent.Proof still finds and attaches the evidence on request.
func TestAgentProofAttachesFromSlashCommand(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess
	if err := a.Run(context.Background(), "add foo", nil); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "add foo")

	text, err := a.Proof(context.Background(), root, "")
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}
	if !strings.Contains(text, "foo.go") {
		t.Fatalf("summary missing foo.go: %s", text)
	}
	sha := headOf(t, root)
	if _, ok, rerr := proof.Read(context.Background(), root, sha); rerr != nil || !ok {
		t.Fatalf("expected /proof to have attached a note: ok=%v err=%v", ok, rerr)
	}

	// A second call finds the note already there and just prints it.
	text2, err := a.Proof(context.Background(), root, sha)
	if err != nil || text2 != text {
		t.Fatalf("second Proof call = %q, %v; want the same summary and no error", text2, err)
	}
}

// TestAgentProofNoEvidence is the case /proof cannot help with: the
// session never touched any file the named commit contains.
func TestAgentProofNoEvidence(t *testing.T) {
	a, root := newAgent(t, &fake{}, nil)
	gitInit(t, root)
	os.WriteFile(filepath.Join(root, "untouched.txt"), []byte("x\n"), 0o644)
	commitAll(t, root, "untouched by this session")

	if _, err := a.Proof(context.Background(), root, ""); err == nil {
		t.Fatal("expected an error when this session made no tracked change to the commit")
	}
}

// TestAgentProofIncludesMutationScore covers the event → record path: a
// mutation-testing run logs a session.TypeMutation event for a turn (as
// verifyGate and /verify's manual run both do via mutate.Report.Event),
// and buildProof folds the latest one for a turn into proof.Turn.Mutation,
// which Summary then prints.
func TestAgentProofIncludesMutationScore(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess
	if err := a.Run(context.Background(), "add foo", nil); err != nil {
		t.Fatal(err)
	}
	// The turn just ran mutation testing; log its result the same way
	// verifyGate and the manual /verify command do.
	a.LogEvent(mutate.Report{Command: "go test ./...", Total: 9, Killed: 7, Survived: []mutate.Outcome{{}, {}}}.Event())
	commitAll(t, root, "add foo")

	got, err := a.Proof(context.Background(), root, "")
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}
	if !strings.Contains(got, "mutations 7/9 caught") {
		t.Fatalf("summary missing mutation score: %s", got)
	}
	sha := headOf(t, root)
	r, ok, err := proof.Read(context.Background(), root, sha)
	if err != nil || !ok {
		t.Fatalf("proof.Read: ok=%v err=%v", ok, err)
	}
	if len(r.Turns) != 1 || r.Turns[0].Mutation == nil {
		t.Fatalf("turns = %+v, want a mutation score", r.Turns)
	}
	if m := r.Turns[0].Mutation; m.Killed != 7 || m.Survived != 2 || m.Total != 9 {
		t.Fatalf("mutation = %+v", m)
	}
}

// TestAutomaticProofDoesNotMisattributeAcrossCommits is defect #2's
// end-to-end regression test: a file written in turn 1 and committed
// (commit A), then edited again in turn 2 and committed again (commit B),
// must not have turn 1's change show up in commit B's note just because
// it is the same path -- each commit's note holds only the change that
// actually landed in it.
func TestAutomaticProofDoesNotMisattributeAcrossCommits(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		toolCall("t2", "bash", `{"command":"git add -A && git commit -m 'add foo'"}`),
		text("turn 1 done"),
		toolCall("t3", "edit_file", `{"path":"foo.go","old_string":"package foo\n","new_string":"package foo\n\nfunc A() {}\n"}`),
		toolCall("t4", "bash", `{"command":"git add -A && git commit -m 'add A'"}`),
		text("turn 2 done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.ProofNotes = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess

	if err := a.Run(context.Background(), "add foo", nil); err != nil {
		t.Fatal(err)
	}
	shaA := headOf(t, root)
	if err := a.Run(context.Background(), "add A", nil); err != nil {
		t.Fatal(err)
	}
	shaB := headOf(t, root)
	if shaA == shaB {
		t.Fatal("the second commit did not move HEAD")
	}

	recA, ok, err := proof.Read(context.Background(), root, shaA)
	if err != nil || !ok {
		t.Fatalf("proof.Read(commit A): ok=%v err=%v", ok, err)
	}
	if len(recA.Files) != 1 || len(recA.Files[0].Changes) != 1 || recA.Files[0].Changes[0].Seq != 1 {
		t.Fatalf("commit A's note = %+v, want only the turn 1 change (#1)", recA.Files)
	}

	recB, ok, err := proof.Read(context.Background(), root, shaB)
	if err != nil || !ok {
		t.Fatalf("proof.Read(commit B): ok=%v err=%v", ok, err)
	}
	if len(recB.Files) != 1 || len(recB.Files[0].Changes) != 1 || recB.Files[0].Changes[0].Seq != 2 {
		t.Fatalf("commit B's note = %+v, want only the turn 2 change (#2)", recB.Files)
	}
}

// TestManualCommitsDoNotMisattributeAcrossCommits covers the same fix
// through the manual path (/proof on request rather than the automatic
// bash-commit hook): a session writes then edits the same file across two
// turns, the user commits each turn's work separately outside the
// session's own tool calls, and /proof on each commit must show only that
// commit's own change.
func TestManualCommitsDoNotMisattributeAcrossCommits(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		text("turn 1 done"),
		toolCall("t2", "edit_file", `{"path":"foo.go","old_string":"package foo\n","new_string":"package foo\n\nfunc A() {}\n"}`),
		text("turn 2 done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess

	if err := a.Run(context.Background(), "add foo", nil); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "add foo")
	shaA := headOf(t, root)

	if err := a.Run(context.Background(), "add A", nil); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "add A")
	shaB := headOf(t, root)

	if _, err := a.Proof(context.Background(), root, shaA); err != nil {
		t.Fatalf("Proof(commit A): %v", err)
	}
	if _, err := a.Proof(context.Background(), root, shaB); err != nil {
		t.Fatalf("Proof(commit B): %v", err)
	}

	recA, ok, err := proof.Read(context.Background(), root, shaA)
	if err != nil || !ok {
		t.Fatalf("proof.Read(commit A): ok=%v err=%v", ok, err)
	}
	if len(recA.Files) != 1 || len(recA.Files[0].Changes) != 1 || recA.Files[0].Changes[0].Seq != 1 {
		t.Fatalf("commit A's note = %+v, want only the turn 1 change (#1)", recA.Files)
	}

	recB, ok, err := proof.Read(context.Background(), root, shaB)
	if err != nil || !ok {
		t.Fatalf("proof.Read(commit B): ok=%v err=%v", ok, err)
	}
	if len(recB.Files) != 1 || len(recB.Files[0].Changes) != 1 || recB.Files[0].Changes[0].Seq != 2 {
		t.Fatalf("commit B's note = %+v, want only the turn 2 change (#2)", recB.Files)
	}
}

// commitAll relies on gitInit (undo_test.go) having already given root a
// repo-local identity via gitxtest, so it needs no GIT_AUTHOR_* env of its
// own.
func commitAll(t *testing.T, root, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestAutomaticProofIncludesTheTurnsGateRuns: the note attached on a
// commit made mid-turn used to be built before the turn-end verify gate
// ran, so the green gate that demonstrated the work was missing from it.
// The note is brought up to date once the turn's gates have run.
func TestAutomaticProofIncludesTheTurnsGateRuns(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
		toolCall("t2", "bash", `{"command":"git add -A && git commit -q -m 'add foo'"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.ProofNotes = true
	a.Root = root
	a.Verify = &verify.Runner{Config: verify.Config{Commands: []string{"true"}}, Cwd: root}
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Log = sess
	if err := a.Run(context.Background(), "add foo", nil); err != nil {
		t.Fatal(err)
	}
	r, ok, err := proof.Read(context.Background(), root, headOf(t, root))
	if err != nil || !ok {
		t.Fatalf("proof.Read: ok=%v err=%v", ok, err)
	}
	if len(r.Turns) != 1 || r.Turns[0].Passed != 1 || strings.Join(r.Turns[0].Checks, "|") != "true" {
		t.Fatalf("turns = %+v, want the verify gate's green run recorded", r.Turns)
	}
	// The same run on the evidence model: the gate ran after the edit, so
	// it is fresh and the turn's evidence is passed.
	if ev := r.Turns[0].Evidence; ev == nil || ev.Status != evidence.Passed || ev.Blocks || len(ev.Checks) != 1 || !ev.Checks[0].Fresh || ev.Checks[0].Source != session.GateVerify {
		t.Fatalf("turn evidence = %+v", ev)
	}
}

// TestAutomaticEvidenceIsSignedAndReproduces is the evidence path end to
// end: the turn's own check and the verify gate's run are recorded in the
// signed statement beside the proof note, and re-running them from a
// throwaway checkout of the commit reproduces both. With the evidence
// behaviour off, the proof note is attached and the evidence is not.
func TestAutomaticEvidenceIsSignedAndReproduces(t *testing.T) {
	for _, off := range []bool{false, true} {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"foo.go","content":"package foo\n"}`),
			toolCall("t2", "bash", `{"command":"test -f foo.go"}`),
			toolCall("t3", "bash", `{"command":"git add -A && git commit -q -m 'add foo'"}`),
			text("done"),
		}}
		a, root := newAgent(t, f, nil)
		gitInit(t, root)
		repo, err := snapshot.Open(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		a.Snapshots = repo
		a.AutoApprove = true
		a.ProofNotes = true
		a.Root = root
		keys := t.TempDir()
		a.Evidence = proof.NewKeySigner(keys)
		if off {
			a.Off = map[string]bool{"evidence": true}
		}
		a.Verify = &verify.Runner{Config: verify.Config{Commands: []string{"true"}}, Cwd: root}
		a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
		sess, err := session.New(t.TempDir(), root, "p", "m")
		if err != nil {
			t.Fatal(err)
		}
		a.Log = sess
		if err := a.Run(context.Background(), "add foo", nil); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, ok, err := proof.Read(ctx, root, headOf(t, root)); err != nil || !ok {
			t.Fatalf("proof note: ok=%v err=%v", ok, err)
		}
		_, ok, err := proof.ReadEvidence(ctx, root, headOf(t, root))
		if err != nil {
			t.Fatal(err)
		}
		if off {
			if ok {
				t.Fatal("evidence attached with the evidence behaviour off")
			}
			continue
		}
		if !ok {
			t.Fatal("no evidence attached beside the proof note")
		}
		trusted, err := proof.TrustedKeys(keys)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := proof.VerifyEvidence(ctx, root, "HEAD", proof.VerifyOptions{Run: true, Trusted: trusted})
		if err != nil {
			t.Fatal(err)
		}
		if !rep.OK() || rep.Count(proof.ClaimReproduced) != 2 || len(rep.Claims) != 2 {
			t.Fatalf("verify:\n%s", proof.VerifySummary(rep))
		}
	}
}
