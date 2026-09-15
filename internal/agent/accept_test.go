package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestAcceptLockDeniesEditFile(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "edit_file", `{"path":"foo_test.go","old_string":"package foo","new_string":"package foo // hacked"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := hashFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "tweak the test", emit); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(testPath); string(data) != "package foo\n" {
		t.Fatalf("a locked test file was changed: %q", data)
	}
	if !anyDenied(*evs, "locked acceptance test") {
		t.Fatalf("no denial mentioning the lock: %+v", evs)
	}
}

func TestAcceptLockDeniesWriteFile(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo_test.go","content":"package foo // hacked\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "tweak the test", emit); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(testPath); string(data) != "package foo\n" {
		t.Fatalf("a locked test file was overwritten: %q", data)
	}
	if !anyDenied(*evs, "locked acceptance test") {
		t.Fatalf("no denial mentioning the lock: %+v", evs)
	}
}

// TestAcceptLockDeniesBashWrite is the "echo x > locked_test.go" case:
// a shell redirect at a locked path is caught the same way as the file
// tools, via shell.WriteTargets sharing safety.go's path-escape analysis.
func TestAcceptLockDeniesBashWrite(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo x > foo_test.go"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "tweak the test", emit); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(testPath); string(data) != "package foo\n" {
		t.Fatalf("a locked test file was changed by bash: %q", data)
	}
	if !anyDenied(*evs, "locked acceptance test") {
		t.Fatalf("no denial mentioning the lock: %+v", evs)
	}
}

// TestAcceptUnlockAllowsEditAndRefreshesHash covers /accept unlock: the
// edit goes through, and the stored hash moves with it so the next
// integrity check treats the new content as expected rather than as
// tampering.
func TestAcceptUnlockAllowsEditAndRefreshesHash(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "edit_file", `{"path":"foo_test.go","old_string":"package foo","new_string":"package foo // fixed"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.SetAccept(&session.AcceptMeta{
		// Command is "true", not a real test command: this test is about
		// the edit and the hash refresh, not the gate run that follows it
		// (that path is covered by TestAcceptGateDetectsTamperAndFailsWithoutRunning).
		Phase: session.AcceptUnlocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "true",
	}, nil)
	if err := a.Run(context.Background(), "fix the test", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "// fixed") {
		t.Fatalf("unlocked edit did not apply: %q", data)
	}
	newSum, err := hashFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if a.AcceptMeta().Hashes[testPath] != newSum {
		t.Fatalf("hash was not refreshed after the unlocked edit: got %s want %s", a.AcceptMeta().Hashes[testPath], newSum)
	}
}

// TestAcceptGateDetectsTamperAndFailsWithoutRunning is the integrity
// check: a locked file changed by something other than a tool call (a
// bash trick that slipped past, an external editor) is reported loudly
// and fails the gate, without even running the test command.
func TestAcceptGateDetectsTamperAndFailsWithoutRunning(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"impl.go","content":"package foo\n"}`),
		text("done"),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	// A command that would obviously succeed: if it ran, Gate.Passed
	// would be true, so seeing it stay nil shows the command never ran.
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "true",
	}, nil)
	// Tamper with the locked file outside any tool call.
	if err := os.WriteFile(testPath, []byte("package foo // tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "add the foo endpoint", emit); err != nil {
		t.Fatal(err)
	}
	if a.AcceptMeta().Tamper == "" {
		t.Fatal("Accept.Tamper was not recorded")
	}
	if a.AcceptMeta().Gate != nil {
		t.Fatalf("the gate command ran despite tampering: %+v", a.AcceptMeta().Gate)
	}
	found := false
	for _, e := range *evs {
		if e.Kind == EvNotice && e.IsError && strings.Contains(e.Text, "integrity check failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no loud integrity-failure notice: %+v", evs)
	}
}

// TestAcceptApproveLocksFilesAndRecordsHashes covers approve: it needs a
// command (the model's own, here) and hashes every file at that moment.
func TestAcceptApproveLocksFilesAndRecordsHashes(t *testing.T) {
	root := t.TempDir()
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "add foo", Root: root,
		Files: []string{testPath}, Proposed: "go test ./...",
	}, nil)
	cmd, err := a.AcceptApprove()
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "go test ./..." {
		t.Fatalf("command = %q", cmd)
	}
	if a.AcceptMeta().Phase != session.AcceptLocked {
		t.Fatalf("phase = %s, want locked", a.AcceptMeta().Phase)
	}
	want, _ := hashFile(testPath)
	if a.AcceptMeta().Hashes[testPath] != want {
		t.Fatalf("hash not recorded for %s", testPath)
	}
}

// TestAcceptWritingPhaseRefusesNonTestFileEdits is the read-only-except-
// tests rule: the model's only job is to write tests, so an attempt to
// write an implementation file is refused, not silently allowed.
func TestAcceptWritingPhaseRefusesNonTestFileEdits(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"impl.go","content":"package foo\n"}`),
		// The denial leaves Files empty, so the harness nudges once for a
		// test file before letting the turn end; the model's reply to
		// that nudge is scripted here rather than acted on, since this
		// test is only checking that the write itself was refused.
		text("okay"),
		text("still no test file written"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	a.AcceptStart("add a foo endpoint", root)
	evs, emit := collect()
	if err := a.Run(context.Background(), "write acceptance tests", emit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "impl.go")); err == nil {
		t.Fatal("a non-test file was written during the writing phase")
	}
	if !anyDenied(*evs, "only test files") {
		t.Fatalf("no denial mentioning test-files-only: %+v", evs)
	}
}

// TestAcceptWritingPhaseCapturesProposedCommand covers the happy path of
// writing tests: a test file goes through, and the ACCEPT-VERIFY line at
// the end of the turn moves the mode to "proposed".
func TestAcceptWritingPhaseCapturesProposedCommand(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo_test.go","content":"package foo\n"}`),
		text("Wrote the acceptance test.\n\nACCEPT-VERIFY: go test ./..."),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	a.AcceptStart("add a foo endpoint", root)
	if err := a.Run(context.Background(), "write acceptance tests", nil); err != nil {
		t.Fatal(err)
	}
	if a.AcceptMeta().Phase != session.AcceptProposed {
		t.Fatalf("phase = %s, want proposed", a.AcceptMeta().Phase)
	}
	if a.AcceptMeta().Proposed != "go test ./..." {
		t.Fatalf("proposed = %q", a.AcceptMeta().Proposed)
	}
	if len(a.AcceptMeta().Files) != 1 || !strings.HasSuffix(a.AcceptMeta().Files[0], "foo_test.go") {
		t.Fatalf("files = %v", a.AcceptMeta().Files)
	}
}

// TestAcceptStateSurvivesResume is the persistence half wired all the
// way through: OnAcceptChange (the same shape as Cron.OnChange, see
// cmd/janetcode's buildAgent) keeps the session record in step as the
// mode moves from writing to locked, a fresh session.Open reads it back
// the way a resumed process would, and a brand new Agent built from that
// restored state still enforces the lock — not just that the bytes
// round-trip, but that they mean the same thing to the harness they did
// before the process restarted.
func TestAcceptStateSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.New(dir, "/proj", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"foo_test.go","content":"package foo\n"}`),
		text("Wrote the acceptance test.\n\nACCEPT-VERIFY: go test ./..."),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	a.SetAccept(nil, func(am *session.AcceptMeta) { _ = sess.SetAccept(am) })
	a.AcceptStart("add a foo endpoint", root)
	if err := a.Run(context.Background(), "write acceptance tests", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcceptApprove(); err != nil {
		t.Fatal(err)
	}
	if sess.Meta.Accept == nil || sess.Meta.Accept.Phase != session.AcceptLocked {
		t.Fatalf("session record after approve: %+v", sess.Meta.Accept)
	}

	// Resume: reopen the session the way a new process would, and build a
	// fresh agent from what it restores — no reference to a or its Accept
	// pointer.
	reopened, _, err := session.Open(dir, sess.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := &Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root}), Model: "m"}
	b.AutoApprove = true
	b.SetAccept(reopened.Meta.Accept, nil)
	if b.AcceptMeta() == nil || b.AcceptMeta().Phase != session.AcceptLocked {
		t.Fatalf("restored accept state: %+v", b.AcceptMeta())
	}

	f2 := &fake{responses: []*llm.Response{
		toolCall("t2", "edit_file", `{"path":"foo_test.go","old_string":"package foo","new_string":"package foo // hacked"}`),
		text("done"),
	}}
	b.Provider = f2
	evs, emit := collect()
	if err := b.Run(context.Background(), "weaken the test", emit); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(root, "foo_test.go")
	if data, _ := os.ReadFile(testPath); !strings.Contains(string(data), "package foo\n") || strings.Contains(string(data), "hacked") {
		t.Fatalf("the resumed agent did not keep the lock: %q", data)
	}
	if !anyDenied(*evs, "locked acceptance test") {
		t.Fatalf("no denial from the resumed agent: %+v", evs)
	}
}

// TestAcceptProposedPhaseRefusesNonTestFileEdits is the enforcement gap a
// real QA run found: once the writing turn ends and the mode moves to
// "proposed", the restriction to test-file writes has to keep holding
// until the user actually runs /accept approve — a model that just goes
// ahead and edits the implementation before approval must be refused the
// same way it would be mid-writing.
func TestAcceptProposedPhaseRefusesNonTestFileEdits(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"impl.go","content":"package foo\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "add foo", Root: root,
		Files: []string{testPath}, Proposed: "go test ./...",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "go ahead and implement it now", emit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "impl.go")); err == nil {
		t.Fatal("a non-test file was written while tests were only proposed, not yet approved")
	}
	if !anyDenied(*evs, "only test files") {
		t.Fatalf("no denial mentioning test-files-only in the proposed phase: %+v", evs)
	}
}

// TestAcceptProposedPhaseRefusesBash covers the same gap for bash: the
// writing phase refuses every bash call that is not a read or a test run
// (it is not a file tool that could touch only test files), and proposed
// must too.
func TestAcceptProposedPhaseRefusesBash(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"touch impl.go"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "add foo", Root: root,
		Files: []string{filepath.Join(root, "foo_test.go")}, Proposed: "go test ./...",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "run something", emit); err != nil {
		t.Fatal(err)
	}
	if !anyDenied(*evs, "only test files") {
		t.Fatalf("bash was not refused in the proposed phase: %+v", evs)
	}
}

// TestAcceptLockedFileRestoredAfterUntrackedBashWrite is the content
// guarantee behind the lock: WriteTargets' static command analysis can
// always be extended, but it can never cover every way to write a file
// (here, `dd of=...` — no mutator name, no `>` redirect for it to catch).
// The lock has to hold anyway: after every mutating tool call the
// harness compares each locked file's hash and restores it from the
// content captured at approve/lock time the moment it moved, whatever
// wrote it, and tells the model plainly that its change was reverted.
func TestAcceptLockedFileRestoredAfterUntrackedBashWrite(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"printf 'tampered\n' | dd of=foo_test.go 2>/dev/null"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	orig := "package foo\n"
	if err := os.WriteFile(testPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum},
		Contents: map[string][]byte{testPath: []byte(orig)},
		Command:  "true",
	}, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "fix the code so the test passes", emit); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != orig {
		t.Fatalf("a locked file bypassed by dd was not restored: %q", data)
	}
	found := false
	for _, e := range *evs {
		if e.Kind == EvToolResult && strings.Contains(e.Output, "was reverted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no revert message reached the model's tool result: %+v", evs)
	}
}

// TestAcceptExternalTamperIsRestoredWithoutBlamingTheCall: a locked file
// changed outside janetcode is put back before the next mutating call
// runs, and the note says it changed outside the session rather than
// blaming that call.
func TestAcceptExternalTamperIsRestoredWithoutBlamingTheCall(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo hi > other.txt"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	orig := "package foo\n"
	if err := os.WriteFile(testPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum},
		Contents: map[string][]byte{testPath: []byte(orig)},
		Command:  "true",
	}, nil)
	if err := os.WriteFile(testPath, []byte("package foo // weakened\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "write other.txt", emit); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(testPath); string(data) != orig {
		t.Fatalf("external tamper not restored: %q", data)
	}
	for _, e := range *evs {
		if e.Kind == EvToolResult && strings.Contains(e.Output, "your bash call") {
			t.Fatalf("the bash call was blamed for a change made outside the session: %q", e.Output)
		}
	}
}

// TestAcceptCaptureStripsBacktickWrappedCommand is the exit-127 bug: a
// model that wraps its ACCEPT-VERIFY command in backticks (or fences the
// whole final message and adds trailing content, as here) must not have
// that wrapping become part of the literal shell command — running the
// backtick-wrapped form performs command substitution, then
// tries to execute the test output's first line as a command, which is
// exactly how "===: command not found" (exit 127) was produced from a
// gate command that ran fine by hand.
func TestAcceptCaptureStripsBacktickWrappedCommand(t *testing.T) {
	a := &Agent{}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptWriting, Request: "slug fix", Files: []string{"foo_test.go"},
	}, nil)
	text := "Wrote the acceptance test.\n\nACCEPT-VERIFY: `cd pkg && go test -v -run TestSlug`\n\n=== RUN   TestSlug\n--- PASS: TestSlug (0.00s)\nPASS\n"
	nudged := false
	if more := a.acceptCapture(text, &nudged, func(Event) {}); more {
		t.Fatal("capture should have moved to proposed, not asked again")
	}
	if strings.ContainsAny(a.AcceptMeta().Proposed, "`") {
		t.Fatalf("proposed command kept its backtick wrapping: %q", a.AcceptMeta().Proposed)
	}
	if a.AcceptMeta().Proposed != "cd pkg && go test -v -run TestSlug" {
		t.Fatalf("proposed command = %q", a.AcceptMeta().Proposed)
	}
}

// TestAcceptGateStopsImmediatelyOnCommandNotFound is the runaway-retry
// bug: a gate command that cannot even run (exit 126/127 — not found, not
// executable) is a bad command, not a failing test, so sending the model
// back to "fix the code" for verify.MaxRounds rounds only burns time. The
// gate must give up on the first such failure and point at
// `/accept command <cmd>` instead of retrying.
func TestAcceptGateStopsImmediatelyOnCommandNotFound(t *testing.T) {
	root := t.TempDir()
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a := &Agent{}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum},
		Command: "this-command-does-not-exist-anywhere-xyz",
	}, nil)
	a.mutations = 1
	g := &gateState{}
	evs, emit := collect()
	if more := a.acceptVerifyGate(context.Background(), g, emit); more {
		t.Fatal("the gate asked for another round for a command that cannot run at all")
	}
	if g.rounds != 0 {
		t.Fatalf("rounds = %d, want 0: an unrunnable command must not spend a retry round", g.rounds)
	}
	hinted := false
	for _, e := range *evs {
		if strings.Contains(e.Text, "/accept command") {
			hinted = true
		}
	}
	if !hinted {
		t.Fatalf("no hint to fix the command via /accept command: %+v", evs)
	}
}

func anyDenied(evs []Event, substr string) bool {
	for _, e := range evs {
		if e.Kind == EvToolResult && e.IsError && e.Denied && strings.Contains(e.Output, substr) {
			return true
		}
	}
	return false
}

// /accept approve is answered in the transcript with the command it
// locked, for the user to confirm, and a second approve says the tests
// are already locked rather than "nothing proposed yet".
func TestAcceptApproveShowsTheCommandAndASecondApproveSaysLocked(t *testing.T) {
	root := t.TempDir()
	testPath := filepath.Join(root, "clip_test.go")
	if err := os.WriteFile(testPath, []byte("package clip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "clip", Root: root,
		Files: []string{testPath}, Proposed: "go test -run TestClip ./...",
	}, nil)
	res, err := a.AcceptCommand(AcceptArgs{Approve: true}, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sheet, "locked") || !strings.Contains(res.Sheet, "go test -run TestClip ./...") || !strings.Contains(res.Sheet, "clip_test.go") {
		t.Fatalf("approve result = %+v, want a sheet naming the files and the locked command", res)
	}
	res, err = a.AcceptCommand(AcceptArgs{Approve: true}, root)
	if err != nil || !strings.Contains(res.Sheet, "already locked") || !strings.Contains(res.Sheet, "go test -run TestClip ./...") {
		t.Fatalf("second approve = %+v, %v, want already locked with the command", res, err)
	}
}

// Tests revised after the command was proposed make that command stale
// (it named a test the model had since dropped): the mode goes back to
// writing, so the turn ends by stating the command again.
func TestAcceptRevisingProposedTestsRecapturesTheCommand(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"clip_test.go","content":"package clip\n\nfunc TestClipBytes(t *testing.T) {}\n"}`),
		text("Dropped TestOneLineExactBoundary.\n\nACCEPT-VERIFY: go test -run TestClipBytes ./..."),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	testPath := filepath.Join(root, "clip_test.go")
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "clip", Root: root,
		Files: []string{testPath}, Proposed: "go test -run 'TestClipBytes|TestOneLineExactBoundary' ./...",
	}, nil)
	if err := a.Run(context.Background(), "drop the boundary test", nil); err != nil {
		t.Fatal(err)
	}
	if a.AcceptMeta().Phase != session.AcceptProposed || a.AcceptMeta().Proposed != "go test -run TestClipBytes ./..." {
		t.Fatalf("accept = %+v, want the revised command proposed", a.AcceptMeta())
	}
}

// While tests are written, reading the tree and running the tests are
// what the model needs: read-only shell and a test run pass the mode gate
// (the approval prompt still applies); anything else is still refused.
func TestAcceptWritingPhaseAllowsReadsAndTestRuns(t *testing.T) {
	a := &Agent{Root: "/work/repo"}
	a.SetAccept(&session.AcceptMeta{Phase: session.AcceptWriting, Request: "clip", Root: "/work/repo"}, nil)
	bash := tool.Bash{Root: "/work/repo"}
	for cmd, allowed := range map[string]bool{
		"find . -name '*_test.go'":                    true,
		"cd /work/repo && grep -rn Clip internal":     true,
		"go test ./internal/textfmt -run TestClip":    true,
		"cd /work/repo && go test ./internal/textfmt": true,
		"touch impl.go":                       false,
		"sed -i s/a/b/ clip_test.go":          false,
		"go test ./... && rm -rf internal":    false,
		"cat > /tmp/trace.go <<'EOF'\nx\nEOF": false,
	} {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		if _, denied := a.toolGate(bash, in); denied == allowed {
			t.Errorf("%q: denied=%v, want allowed=%v", cmd, denied, allowed)
		}
	}
	// Proposed: the proposed command itself runs too.
	proposed := a.AcceptMeta()
	proposed.Phase, proposed.Proposed = session.AcceptProposed, "make acceptance"
	a.SetAccept(proposed, nil)
	in, _ := json.Marshal(map[string]string{"command": "make acceptance"})
	if _, denied := a.toolGate(bash, in); denied {
		t.Error("the proposed command was refused in the proposed phase")
	}
}

// The writing phase's red tests are its expected end state: the project's
// verify gate does not send the model back to "fix" them, and the turn's
// verdict says the tests were written rather than that a check failed.
func TestAcceptWritingTurnIsNotSentBackOrJudgedFailed(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"clip_test.go","content":"package clip\n"}`),
		toolCall("t2", "bash", `{"command":"go test ./nonexistent-package-xyz"}`),
		text("Tests written; they fail until ClipBytes exists.\n\nACCEPT-VERIFY: go test ./..."),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Root = root
	a.Log = &session.Memory{}
	a.Verify = &verify.Runner{Config: verify.Config{Commands: []string{"false"}}, Cwd: root}
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	a.AcceptStart("clip bytes", root)
	evs, emit := collect()
	if err := a.Run(context.Background(), "write acceptance tests", emit); err != nil {
		t.Fatal(err)
	}
	var verdict string
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "sending the model back") {
			t.Fatalf("the writing phase was sent back to fix its red tests: %s", e.Text)
		}
		if e.Kind == EvVerdict {
			verdict = e.Text
		}
	}
	if !strings.Contains(verdict, string(VerdictTestsWritten)) || strings.Contains(verdict, "✗") {
		t.Fatalf("verdict = %q, want tests written", verdict)
	}
}

// hashFile is the sha256 of a file's content, the same thing accept mode
// records for a locked test; here so these tests can check a hash without
// reaching into that package.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
