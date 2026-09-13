package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
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
	a.Accept = &session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}
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
	a.Accept = &session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}
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
// tools, via tool.WriteTargets sharing safety.go's path-escape analysis.
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
	a.Accept = &session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}
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
	a.Accept = &session.AcceptMeta{
		// Command is "true", not a real test command: this test is about
		// the edit and the hash refresh, not the gate run that follows it
		// (that path is covered by TestAcceptGateDetectsTamperAndFailsWithoutRunning).
		Phase: session.AcceptUnlocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "true",
	}
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
	if a.Accept.Hashes[testPath] != newSum {
		t.Fatalf("hash was not refreshed after the unlocked edit: got %s want %s", a.Accept.Hashes[testPath], newSum)
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
	a.Accept = &session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "true",
	}
	// Tamper with the locked file outside any tool call.
	if err := os.WriteFile(testPath, []byte("package foo // tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "add the foo endpoint", emit); err != nil {
		t.Fatal(err)
	}
	if a.Accept.Tamper == "" {
		t.Fatal("Accept.Tamper was not recorded")
	}
	if a.Accept.Gate != nil {
		t.Fatalf("the gate command ran despite tampering: %+v", a.Accept.Gate)
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
	a := &Agent{Accept: &session.AcceptMeta{
		Phase: session.AcceptProposed, Request: "add foo", Root: root,
		Files: []string{testPath}, Proposed: "go test ./...",
	}}
	cmd, err := a.AcceptApprove()
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "go test ./..." {
		t.Fatalf("command = %q", cmd)
	}
	if a.Accept.Phase != session.AcceptLocked {
		t.Fatalf("phase = %s, want locked", a.Accept.Phase)
	}
	want, _ := hashFile(testPath)
	if a.Accept.Hashes[testPath] != want {
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
	if a.Accept.Phase != session.AcceptProposed {
		t.Fatalf("phase = %s, want proposed", a.Accept.Phase)
	}
	if a.Accept.Proposed != "go test ./..." {
		t.Fatalf("proposed = %q", a.Accept.Proposed)
	}
	if len(a.Accept.Files) != 1 || !strings.HasSuffix(a.Accept.Files[0], "foo_test.go") {
		t.Fatalf("files = %v", a.Accept.Files)
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
	a.OnAcceptChange = func(am *session.AcceptMeta) { _ = sess.SetAccept(am) }
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
	b.Accept = reopened.Meta.Accept
	if b.Accept == nil || b.Accept.Phase != session.AcceptLocked {
		t.Fatalf("restored accept state: %+v", b.Accept)
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

func anyDenied(evs []Event, substr string) bool {
	for _, e := range evs {
		if e.Kind == EvToolResult && e.IsError && e.Denied && strings.Contains(e.Output, substr) {
			return true
		}
	}
	return false
}
