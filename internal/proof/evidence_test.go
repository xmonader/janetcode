package proof

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The same suite run twice — in the session's workspace and in verify's
// throwaway checkout, with different timings — must hash the same; a
// different test outcome must not.
func TestOutputDigestIgnoresTimingsRootAndHarnessLines(t *testing.T) {
	a := "\x1b[32mok\x1b[0m  \texample.com/foo\t0.012s\n=== RUN TestA\n--- PASS: TestA (0.00s)\nPASS\n"
	b := "ok  \texample.com/foo\t(cached)\n=== RUN TestA\n--- PASS: TestA (1.37s)\nPASS\n\n[exit code 0]"
	da, pa := OutputDigest(a, "/home/u/proj")
	db, pb := OutputDigest(b, "/tmp/janetcode-verify-123")
	if da != db || pa || pb {
		t.Fatalf("same outcome hashed differently:\n%q\n%q\n%s %s", NormalizeOutput(a, ""), NormalizeOutput(b, ""), da, db)
	}
	c := "ok  \texample.com/foo\t0.012s\n=== RUN TestA\n--- FAIL: TestA (0.00s)\nFAIL\n"
	if dc, _ := OutputDigest(c, ""); dc == da {
		t.Fatal("a failing test hashed the same as a passing one")
	}
	root := "/home/u/proj"
	x, _ := OutputDigest("open "+root+"/a.go: no such file", root)
	y, _ := OutputDigest("open /tmp/janetcode-verify-9/a.go: no such file", "/tmp/janetcode-verify-9")
	if x != y {
		t.Fatalf("the checkout path was not normalised: %q vs %q", NormalizeOutput("open "+root+"/a.go", root), NormalizeOutput("open /tmp/janetcode-verify-9/a.go", "/tmp/janetcode-verify-9"))
	}
	if _, partial := OutputDigest("head\n... [123 bytes omitted; the output continues] ...\ntail", ""); !partial {
		t.Fatal("a cut output was not marked partial")
	}
	// A line number is part of the outcome, not noise.
	l1, _ := OutputDigest("foo_test.go:12: want 3", "")
	l2, _ := OutputDigest("foo_test.go:13: want 3", "")
	if l1 == l2 {
		t.Fatal("line numbers were normalised away")
	}
}

func TestSignAndCheckSignature(t *testing.T) {
	dir := t.TempDir()
	signer := NewKeySigner(dir)
	st := Statement{Type: StatementType, PredicateType: PredicateType,
		Subject:   []Subject{{Name: SubjectName, Digest: map[string]string{"gitTree": "abc"}}},
		Predicate: Evidence{Version: EvidenceVersion, Commit: "c1"}}
	env, err := SignStatement(st, signer)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := TrustedKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s, id := CheckSignature(env, trusted); s != SigValid || !strings.HasPrefix(id, "ed25519:") {
		t.Fatalf("CheckSignature = %s %s, want valid", s, id)
	}
	// A key this machine does not trust: no verdict either way.
	if s, _ := CheckSignature(env, nil); s != SigUntrusted {
		t.Fatalf("untrusted: %s", s)
	}
	// The payload changed after signing.
	back, _ := env.Statement()
	back.Predicate.Commit = "c2"
	raw, _ := json.Marshal(back)
	tampered := env
	tampered.Payload = base64.StdEncoding.EncodeToString(raw)
	if s, _ := CheckSignature(tampered, trusted); s != SigInvalid {
		t.Fatalf("tampered payload: %s, want invalid", s)
	}
	// A signature over the same bytes under another payload type does
	// not carry over (the PAE binds the type).
	retyped := env
	retyped.PayloadType = "application/json"
	if s, _ := CheckSignature(retyped, trusted); s != SigInvalid {
		t.Fatalf("retyped: %s, want invalid", s)
	}
	if _, err := retyped.Statement(); err == nil {
		t.Fatal("Statement accepted a foreign payload type")
	}
}

func TestKeySignerKeepsOneKeyAndTrustedFileIsChecked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	id1, err := NewKeySigner(dir).KeyID()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v, want 0600", fi.Mode().Perm())
	}
	id2, _ := NewKeySigner(dir).KeyID()
	if id1 != id2 {
		t.Fatalf("a second signer made a new key: %s vs %s", id1, id2)
	}
	other := t.TempDir()
	pub, err := NewKeySigner(other).PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	line := TrustLine(pub)
	_, b64, _ := strings.Cut(line, " ")
	forged := "ed25519:0000000000000000 " + b64
	if err := os.WriteFile(filepath.Join(dir, trustedFile), []byte("# comment\n"+forged+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trusted, err := TrustedKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 2 || trusted[KeyIDOf(pub)] == nil || trusted[id1] == nil {
		t.Fatalf("trusted = %v, want the local key and the one well-formed line", trusted)
	}
	// verify never creates a key.
	empty := filepath.Join(t.TempDir(), "none")
	if k, err := TrustedKeys(empty); err != nil || len(k) != 0 {
		t.Fatalf("TrustedKeys on an empty dir = %v %v", k, err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("TrustedKeys created the key directory")
	}
}

func userMsg(turn int, text string) session.Event {
	m := llm.TextMessage(llm.RoleUser, text)
	return session.Event{Type: session.TypeUserMessage, Turn: turn, Message: &m}
}

func bashResult(seq, turn int, id, cmd, out string) session.Event {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return session.Event{Seq: seq, Type: session.TypeToolResult, Turn: turn, CallID: id, Name: "bash", Arguments: string(args), Output: out}
}

func TestSessionFromEventsFoldsWhatTheTurnRan(t *testing.T) {
	root := "/w/proj"
	gr, _ := json.Marshal(GateRun{Exit: 0, Output: "sha256:gate"})
	served := &llm.Provenance{Provider: "moonshot"}
	events := []session.Event{
		userMsg(1, "add foo"),
		{Seq: 2, Type: session.TypeAssistant, Turn: 1, Served: served, Mismatch: "served by moonshot, not xiaomi"},
		bashResult(3, 1, "c1", "cd /w/proj && go test ./...", "--- FAIL: TestFoo\n[exit code 1]"),
		{Seq: 4, Type: session.TypePermission, Turn: 1, CallID: "c2", Name: "edit_file", Reason: "mode: auto-edits"},
		{Seq: 5, Type: session.TypeFileChange, Turn: 1, CallID: "c2", Name: "/w/proj/foo.go", Arguments: "a->b"},
		{Seq: 6, Type: session.TypePermission, Turn: 1, CallID: "c3", Name: "write_file", Reason: "the user: allow"},
		{Seq: 7, Type: session.TypeFileChange, Turn: 1, CallID: "c3", Name: "/w/proj/scratch.txt", Arguments: "absent->c"},
		bashResult(8, 1, "c4", "go test ./...", "ok  example.com/foo 0.01s"),
		bashResult(9, 1, "c5", "ls -la", "foo.go"),                                               // not a check
		bashResult(10, 1, "c6", "pytest -q", "bash: pytest: command not found\n[exit code 127]"), // never ran
		{Seq: 11, Type: session.TypeToolResult, Turn: 1, CallID: "c7", Name: "bash", Arguments: `{"command":"make test"}`, Denied: true},
		{Seq: 12, Type: session.TypeGate, Turn: 1, Name: "go vet ./...", Source: session.GateVerify, Extra: gr},
		{Seq: 13, Type: session.TypeGate, Turn: 1, Name: "acceptance tests", Source: session.GateAccept},
		bashResult(14, 1, "c8", "go build ./...", "(no output)"),
		{Seq: 15, Type: session.TypeFileChange, Turn: 1, CallID: "c9", Name: "/w/proj/foo.go", Arguments: "b->d"},
		bashResult(16, 2, "c10", "go test ./...", "ok"), // another turn
	}
	rec := Record{Session: "s1", Model: "mimo",
		Files: []File{{Path: "foo.go", Changes: []ChangeRef{{Seq: 1, Turn: 1}}}},
		Turns: []Turn{{Turn: 1, Verdict: "verified"}}}
	se := SessionFromEvents(rec, EvidenceInput{
		Session: "s1", Model: "mimo", SystemPrompt: "sys", RepoRoot: root, WorkRoot: root,
		Events: events, Claims: map[int]string{1: "all tests pass"},
	})
	if se.Sandbox.Profile != NoSandbox || se.Workdir != "." || se.SystemPromptSHA256 == "" {
		t.Fatalf("session header = %+v", se)
	}
	if se.Model.Provenance != "mismatch" || len(se.Model.Mismatches) != 1 || len(se.Model.Calls) != 1 || se.Model.Calls[0].Model != "mimo" {
		t.Fatalf("model = %+v", se.Model)
	}
	if len(se.Turns) != 1 {
		t.Fatalf("turns = %+v, want only the contributing turn", se.Turns)
	}
	tu := se.Turns[0]
	if tu.PromptSHA256 != sha256Hex([]byte("add foo")) || tu.Claimed != "all tests pass" || tu.Verdict != "verified" {
		t.Fatalf("turn header = %+v", tu)
	}
	got := map[string]CommandEvidence{}
	for _, c := range tu.Commands {
		got[c.Command] = c
	}
	if len(got) != 4 {
		t.Fatalf("commands = %+v, want go test, go vet, the accept gate, go build", tu.Commands)
	}
	// The red run and the green re-run (one with the workspace cd) are the
	// same command; the latest run is what is recorded.
	if c := got["go test ./..."]; c.Exit != 0 || c.Source != "bash" || c.Output == "" || !c.Stale {
		t.Fatalf("go test = %+v", c)
	}
	if c := got["go vet ./..."]; c.Exit != 0 || c.Output != "sha256:gate" || c.Source != session.GateVerify || c.Sandbox != NoSandbox {
		t.Fatalf("gate = %+v", c)
	}
	if c := got["acceptance tests"]; !c.Withheld {
		t.Fatalf("accept gate = %+v, want withheld", c)
	}
	if c := got["go build ./..."]; !c.Stale {
		t.Fatalf("go build ran before the last edit but is not stale: %+v", c)
	}
	if len(tu.Approvals) != 1 || tu.Approvals[0].Call != "c2" || tu.Approvals[0].Basis != "mode: auto-edits" || tu.Approvals[0].Paths[0] != "foo.go" {
		t.Fatalf("approvals = %+v, want only the call that changed a committed file", tu.Approvals)
	}
}

// A test file the commit rewrote is recorded with its pre-change hash;
// one it added is "absent"; a source file is not listed.
func TestBuildEvidenceRecordsTestsBefore(t *testing.T) {
	versions := map[string]FileVersions{
		"foo_test.go": {Parent: []byte("old"), Commit: []byte("new")},
		"bar_test.go": {Commit: []byte("new")},
		"foo.go":      {Parent: []byte("x"), Commit: []byte("y")},
	}
	st := BuildEvidence("c", "t", Record{}, versions, EvidenceInput{})
	tb := st.Predicate.TestsBefore
	if len(tb) != 2 || tb[0].Path != "bar_test.go" || tb[0].Digest != "absent" || tb[1].Digest != "sha256:"+sha256Hex([]byte("old")) {
		t.Fatalf("tests before = %+v", tb)
	}
	if st.subjectTree() != "t" || st.Type != StatementType || st.PredicateType != PredicateType {
		t.Fatalf("statement = %+v", st)
	}
}

// evidenceRepo makes a repository with one commit and signed evidence on
// it built from cmds, returning the repo, the commit and the key dir.
func evidenceRepo(t *testing.T, turns []TurnEvidence, extra ...SessionEvidence) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "a.txt", "hello\n")
	sha := commit(t, root, "add a")
	tree, err := TreeOf(ctx, root, sha)
	if err != nil {
		t.Fatal(err)
	}
	st := BuildEvidence(sha, tree, Record{}, nil, EvidenceInput{})
	st.Predicate.Sessions = append([]SessionEvidence{{Session: "s1", Workdir: ".", Sandbox: SandboxEvidence{Profile: NoSandbox}, Turns: turns}}, extra...)
	keys := t.TempDir()
	if _, err := AttachEvidence(ctx, root, sha, st, NewKeySigner(keys)); err != nil {
		t.Fatal(err)
	}
	return root, sha, keys
}

func digestOf(out string) string { d, _ := OutputDigest(out, ""); return d }

func TestVerifyEvidenceReRunsAndDiffs(t *testing.T) {
	ctx := context.Background()
	turns := []TurnEvidence{
		{Turn: 1, Commands: []CommandEvidence{
			{Command: "cat a.txt", Source: "bash", Exit: 0, Output: digestOf("hello\n"), Sandbox: NoSandbox},
			{Command: "test -f a.txt", Source: "verify", Exit: 0, Sandbox: NoSandbox},
			{Command: "echo changed", Source: "bash", Exit: 0, Output: digestOf("original"), Sandbox: NoSandbox},
			{Command: "test -f missing.txt", Source: "bash", Exit: 0, Output: digestOf(""), Sandbox: NoSandbox},
			{Command: "exit 3", Source: "verify", Exit: ExitUnknownFailure, Sandbox: NoSandbox},
			{Command: "acceptance tests", Source: "accept", Withheld: true, Sandbox: NoSandbox},
		}},
		{Turn: 2, Claimed: "all tests pass"},
	}
	root, sha, keys := evidenceRepo(t, turns)
	trusted, err := TrustedKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := VerifyEvidence(ctx, root, "HEAD", VerifyOptions{Run: true, Trusted: trusted})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Commit != sha || rep.Signature != SigValid || !rep.SubjectOK {
		t.Fatalf("header = %+v", rep)
	}
	want := map[string]ClaimStatus{
		"cat a.txt":               ClaimReproduced,
		"test -f a.txt":           ClaimExitOnly,
		"echo changed":            ClaimOutputDiffers,
		"test -f missing.txt":     ClaimDiverged,
		"exit 3":                  ClaimExitOnly,
		"acceptance tests":        ClaimNotRun,
		"claimed: all tests pass": ClaimUnverified,
	}
	if len(rep.Claims) != len(want) {
		t.Fatalf("claims = %+v", rep.Claims)
	}
	for _, c := range rep.Claims {
		if want[c.What] != c.Status {
			t.Errorf("%s: %s (%s), want %s", c.What, c.Status, c.Detail, want[c.What])
		}
	}
	if rep.OK() {
		t.Fatal("a diverged claim left the report OK")
	}
	exact, exit, n := rep.Rate()
	if n != 5 || exact != 0.2 || exit != 0.8 {
		t.Fatalf("rate = %v %v %d", exact, exit, n)
	}
	sum := VerifySummary(rep)
	for _, w := range []string{"signature: valid", "diverged", "unverified assertion", "re-ran 5"} {
		if !strings.Contains(sum, w) {
			t.Errorf("summary lacks %q:\n%s", w, sum)
		}
	}
	// Static: nothing runs, the record is still read.
	called := false
	srep, err := VerifyEvidence(ctx, root, "HEAD", VerifyOptions{Trusted: trusted, Exec: func(context.Context, string, string, string, string) (int, string, error) {
		called = true
		return 0, "", nil
	}})
	if err != nil || called || srep.Count(ClaimNotRun) != 6 || srep.Count(ClaimUnverified) != 1 {
		t.Fatalf("static: called=%v claims=%+v err=%v", called, srep.Claims, err)
	}
}

// Evidence moved to a commit with another tree (an amend that changed a
// file carries the note along) no longer holds, and nothing is re-run
// against a tree it does not describe.
func TestVerifyEvidenceRefusesAnotherTree(t *testing.T) {
	ctx := context.Background()
	root, _, keys := evidenceRepo(t, []TurnEvidence{{Turn: 1, Commands: []CommandEvidence{{Command: "true", Source: "bash", Sandbox: NoSandbox}}}})
	write(t, root, "a.txt", "changed\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "--amend", "-m", "add a (amended)")
	trusted, _ := TrustedKeys(keys)
	rep, err := VerifyEvidence(ctx, root, "HEAD", VerifyOptions{Run: true, Trusted: trusted, Exec: func(context.Context, string, string, string, string) (int, string, error) {
		t.Fatal("re-ran a command against a tree the evidence is not about")
		return 0, "", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasEvidence || rep.SubjectOK || rep.OK() {
		t.Fatalf("amended commit: %+v", rep)
	}
}

// A commit with a proof note and no evidence: every check the note lists
// is an unverified assertion, and the evidence does not hold.
func TestVerifyProofWithoutEvidence(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	sha := commit(t, root, "a")
	if err := Attach(ctx, root, sha, Record{Version: Version, Session: "s", Files: []File{{Path: "a.txt"}},
		Turns: []Turn{{Turn: 1, Verdict: "verified", Checks: []string{"go test ./..."}}}}); err != nil {
		t.Fatal(err)
	}
	rep, err := VerifyEvidence(ctx, root, sha, VerifyOptions{Run: true, Trusted: nil})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasEvidence || rep.OK() || rep.Count(ClaimUnverified) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(VerifySummary(rep), "no signed evidence") {
		t.Fatal(VerifySummary(rep))
	}
}

// A second session's evidence for the same commit is added beside the
// first; the same session attaching again replaces its own entry.
func TestAttachEvidenceMergesBySession(t *testing.T) {
	ctx := context.Background()
	root, sha, keys := evidenceRepo(t, []TurnEvidence{{Turn: 1}})
	tree, _ := TreeOf(ctx, root, sha)
	signer := NewKeySigner(keys)
	mk := func(sess string, turn int) Statement {
		st := BuildEvidence(sha, tree, Record{}, nil, EvidenceInput{})
		st.Predicate.Sessions = []SessionEvidence{{Session: sess, Turns: []TurnEvidence{{Turn: turn}}}}
		st.Predicate.Extensions = map[string]json.RawMessage{"integrity": json.RawMessage(`{"assertions_modified":0}`)}
		return st
	}
	if _, err := AttachEvidence(ctx, root, sha, mk("s2", 4), signer); err != nil {
		t.Fatal(err)
	}
	env, err := AttachEvidence(ctx, root, sha, mk("s1", 9), signer)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := env.Statement()
	if len(st.Predicate.Sessions) != 2 || st.Predicate.Sessions[0].Session != "s1" || st.Predicate.Sessions[0].Turns[0].Turn != 9 {
		t.Fatalf("sessions = %+v", st.Predicate.Sessions)
	}
	if string(st.Predicate.Extensions["integrity"]) == "" {
		t.Fatal("the extension was dropped")
	}
	trusted, _ := TrustedKeys(keys)
	if s, _ := CheckSignature(env, trusted); s != SigValid {
		t.Fatalf("merged envelope signature: %s", s)
	}
}

func TestParseArgsVerifyAndKey(t *testing.T) {
	if a := ParseArgs("verify HEAD~1"); a.Action != ActionVerify || a.Commit != "HEAD~1" {
		t.Fatalf("%+v", a)
	}
	if a := ParseArgs("verify"); a.Action != ActionVerify || a.Commit != "" {
		t.Fatalf("%+v", a)
	}
	if a := ParseArgs("key"); a.Action != ActionKey {
		t.Fatalf("%+v", a)
	}
}

// The evidence ref travels with the proof notes through Push and Fetch.
func TestPushFetchCarriesEvidence(t *testing.T) {
	ctx := context.Background()
	bare := t.TempDir()
	gitxtest.InitBareRepo(t, bare)
	origin := t.TempDir()
	gitIn(t, origin, "clone", "-q", bare, ".")
	gitxtest.Configure(t, origin)
	write(t, origin, "foo.go", "package foo\n")
	sha := commit(t, origin, "add foo")
	gitIn(t, origin, "push", "-q", "origin", "HEAD")
	if err := Attach(ctx, origin, sha, Record{Version: Version, Session: "s", Files: []File{{Path: "foo.go"}}}); err != nil {
		t.Fatal(err)
	}
	// A remote with proof notes but no evidence fetches cleanly.
	if _, err := Push(ctx, origin, "origin"); err != nil {
		t.Fatalf("Push without evidence: %v", err)
	}
	other := t.TempDir()
	gitIn(t, other, "clone", "-q", bare, ".")
	if err := Fetch(ctx, other, "origin"); err != nil {
		t.Fatalf("Fetch without evidence: %v", err)
	}
	tree, _ := TreeOf(ctx, origin, sha)
	if _, err := AttachEvidence(ctx, origin, sha, BuildEvidence(sha, tree, Record{}, nil, EvidenceInput{}), NewKeySigner(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, origin, "origin"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := Fetch(ctx, other, "origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, ok, err := ReadEvidence(ctx, other, sha); err != nil || !ok {
		t.Fatalf("evidence after fetch: ok=%v err=%v", ok, err)
	}
}
