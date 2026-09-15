package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
)

// fakeResumeProvider is a minimal scripted llm.Provider, the same shape
// as internal/agent's own test fake but written here since that one is
// unexported to its package.
type fakeResumeProvider struct {
	responses []*llm.Response
	calls     int
}

func (f *fakeResumeProvider) Name() string { return "fake" }
func (f *fakeResumeProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	i := f.calls
	f.calls++
	if i >= len(f.responses) {
		return nil, errors.New("fakeResumeProvider: no more scripted responses")
	}
	r := f.responses[i]
	for _, b := range r.Message.Blocks {
		switch b.Type {
		case llm.BlockText:
			emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
		case llm.BlockToolUse:
			bb := b
			emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
			emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
		}
	}
	return r, nil
}

func resumeText(s string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, s), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}}
}

func resumeToolCall(id, name, input string) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}
}

// TestResumedSessionEnforcesAcceptLock builds a session with locked
// accept state the way a real one gets there (session.New, SetAccept),
// then reopens and wires an agent from it exactly the way --resume does
// (session.Open followed by buildAgent, both from cmd/janetcode) rather
// than constructing an *agent.Agent by hand. It checks the two halves of
// the acceptance-tests lock survive a process restart together: an
// edit_file on the locked test file is refused, and a bash command that
// tampers with it despite that (a `dd` write, matching the untracked-
// write regression in internal/agent) is restored, not merely reported.
func TestResumedSessionEnforcesAcceptLock(t *testing.T) {
	cwd := t.TempDir()
	testPath := filepath.Join(cwd, "foo_test.go")
	orig := "package foo\n\nfunc TestFoo(t *testing.T) {}\n"
	if err := os.WriteFile(testPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	sessDir := t.TempDir()
	sess, err := session.New(sessDir, cwd, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	sum, contents := acceptFixture(t, testPath)
	if err := sess.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add a foo endpoint", Root: cwd,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum},
		Contents: map[string][]byte{testPath: contents}, Command: "true",
	}); err != nil {
		t.Fatal(err)
	}

	// Reopen the way --resume does: session.Open, then buildAgent from
	// what it restores — no reference to sess or its Accept pointer.
	reopened, history, err := session.Open(sessDir, sess.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Meta.Accept == nil || reopened.Meta.Accept.Phase != session.AcceptLocked {
		t.Fatalf("accept state did not round-trip through the session file: %+v", reopened.Meta.Accept)
	}

	fake := &fakeResumeProvider{responses: []*llm.Response{
		resumeToolCall("t1", "edit_file", `{"path":"foo_test.go","old_string":"func TestFoo","new_string":"func TestFoo // weakened"}`),
		resumeText("ok, tried that"),
	}}
	mem := memory.Open(t.TempDir(), cwd)
	deps := agentDeps{
		prov:    fake,
		cfg:     &config.Config{Model: "m", AutoApprove: true},
		cwd:     cwd,
		cfgDir:  t.TempDir(),
		sessDir: sessDir,
		mem:     mem,
	}
	built, err := buildAgent(deps, reopened, history)
	if err != nil {
		t.Fatal(err)
	}
	defer built.close()
	if meta := built.agent.AcceptMeta(); meta == nil || meta.Phase != session.AcceptLocked {
		t.Fatalf("the agent built for the resumed session does not have the lock: %+v", meta)
	}

	var evs []agent.Event
	emit := func(e agent.Event) { evs = append(evs, e) }
	if err := built.agent.Run(context.Background(), "weaken the acceptance test", emit); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != orig {
		t.Fatalf("a resumed session's edit_file on the locked test file went through: %q", data)
	}
	denied := false
	for _, e := range evs {
		if e.Kind == agent.EvToolResult && e.IsError && e.Denied && strings.Contains(e.Output, "locked acceptance test") {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("no denial mentioning the lock after resume: %+v", evs)
	}

	// Now the bash-tamper half, on a fresh turn with a second scripted
	// provider: a write that the pre-execution checks do not recognise
	// (dd, not a redirect and not a listed mutator) must still be
	// restored after the fact.
	fake2 := &fakeResumeProvider{responses: []*llm.Response{
		resumeToolCall("t2", "bash", `{"command":"printf 'tampered\n' | dd of=foo_test.go 2>/dev/null"}`),
		resumeText("done"),
	}}
	built.agent.Provider = fake2
	var evs2 []agent.Event
	emit2 := func(e agent.Event) { evs2 = append(evs2, e) }
	if err := built.agent.Run(context.Background(), "fix the code so it passes", emit2); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != orig {
		t.Fatalf("a resumed session did not restore a locked file bypassed by dd: %q", data)
	}
	restored := false
	for _, e := range evs2 {
		if e.Kind == agent.EvToolResult && strings.Contains(e.Output, "was reverted") {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("no revert message reached the model after resume: %+v", evs2)
	}
}

// acceptFixture reads path and returns its sha256 and content, the same
// pair accept.Mode's own rehash/captureContents would record when locking
// it — this test builds the session file by hand (it is checking resume,
// not /accept approve itself), so it has to match that shape.
func acceptFixture(t *testing.T, path string) (sum string, data []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), data
}
