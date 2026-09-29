// Package accept is the /accept mode: "acceptance tests you approve
// first, that the model can't weaken."
//
// /accept <request> puts the session in a test-first mode: the model's
// only job is to write acceptance tests for the request — read-only
// otherwise — then stop. The user reviews the tests (as a diff, with the
// front-end's existing /diff) and runs /accept approve, which locks the
// test files: from then on the harness refuses any tool call that would
// change them, whatever the model tries, and their run command becomes
// an extra verification gate for this session. /accept unlock lifts the
// lock when a test itself turns out wrong; /accept lock reapplies it;
// /accept off ends the mode.
//
// State lives on session.AcceptMeta, persisted through OnChange the same
// way Cron persists armed loops: the front end that built the agent wires
// the callback to session.Session.SetAccept, so this package never needs
// its own *session.Session reference.
//
// The mode is spread over: cmd.go (the /accept commands), lock.go
// (refusing and restoring changes to locked files), testcmd.go (guessing
// a test command), and this file (the state, the phase and the system
// prompt text). The turn-end verification gate and the ACCEPT-VERIFY
// capture stay in package agent, which owns the turn they happen in;
// they drive this package through Capture, GateTarget and RecordGate.
package accept

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Mode is the /accept state and everything that reads or changes it. The
// zero Mode is accept mode switched off.
//
// It is shared between goroutines: a front-end changes it (/accept
// approve, lock, off) while a turn may be running tools that consult it.
// mu guards the pointer and everything under it. It is only ever held for
// bookkeeping and file hashing — never across a model call or the gate's
// test run — so it cannot stall an approval prompt.
type Mode struct {
	mu   sync.Mutex
	meta *session.AcceptMeta
	// OnChange, when set, is called after the state changes so the
	// session record can keep it — the same shape as Cron.OnChange. It
	// gets a copy, since the session keeps what it is given and this
	// mode goes on changing its own.
	OnChange func(*session.AcceptMeta)
}

// New builds a Mode around the state a resumed session carries (nil for a
// fresh one) and the callback that persists later changes.
func New(meta *session.AcceptMeta, onChange func(*session.AcceptMeta)) *Mode {
	return &Mode{meta: meta, OnChange: onChange}
}

// SetOnChange replaces the persistence callback, for when the session
// that keeps the state changes under a live mode (a plain /fork).
func (m *Mode) SetOnChange(f func(*session.AcceptMeta)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.OnChange = f
}

// Replace swaps in another session's state and the callback that keeps
// it, for when the live mode moves to a different session (/clear,
// /resume). It does not fire: the state came from that session's record
// and is already there, and the session left behind keeps its own.
func (m *Mode) Replace(meta *session.AcceptMeta, f func(*session.AcceptMeta)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meta, m.OnChange = meta.Clone(), f
}

// Meta is a copy of the current state, or nil when the mode is off.
func (m *Mode) Meta() *session.AcceptMeta {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meta.Clone()
}

// On reports whether accept mode is in use at all. It reads the pointer
// under the lock rather than going through Meta, which would deep-copy
// the whole state — files, hashes and every locked file's content — to
// answer a question about nil. It is asked on every mutating tool call.
func (m *Mode) On() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meta != nil
}

// persist tells the front end the state changed. Called with mu held.
func (m *Mode) persist() {
	if m.OnChange != nil {
		m.OnChange(m.meta.Clone())
	}
}

// Phase is the current phase, and false when accept mode is off.
func (m *Mode) Phase() (session.AcceptPhase, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return "", false
	}
	return m.meta.Phase, true
}

// SystemText is appended to the system prompt while accept mode is
// on, so the model is told the rules without paying for them once the
// mode ends (see systemPrompt in context.go).
func (m *Mode) SystemText() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return ""
	}
	switch m.meta.Phase {
	case session.AcceptWriting:
		return fmt.Sprintf("\n\n# Acceptance tests (writing)\nThe user wants acceptance tests for this request, approved before any implementation:\n\n%s\n\nWrite only test files: ones that fail against the code as it stands and would pass once the request is done. No implementation changes — every other mutating tool is refused while this phase lasts, except read-only shell commands and running the tests. When the tests are ready, end your final message with one line of the exact form:\nACCEPT-VERIFY: <command that runs the tests you wrote>\nThe user reviews the tests as a diff and runs /accept approve; only then does work on the implementation start.", m.meta.Request)
	case session.AcceptProposed:
		return fmt.Sprintf("\n\n# Acceptance tests (proposed)\nAcceptance tests for %q are written and awaiting the user's /accept approve (proposed verification command: %s). Do not start implementing until it is approved.", m.meta.Request, m.meta.Proposed)
	case session.AcceptLocked:
		return fmt.Sprintf("\n\n# Acceptance tests (locked)\nThe acceptance tests for %q are approved and locked: %s. Any edit_file, write_file or bash command that would change them is refused — implement the code so they pass instead. They also gate this session: a turn is not done until `%s` passes. If a test is actually wrong, tell the user to run /accept unlock.", m.meta.Request, strings.Join(relFiles(m.meta.Files, m.meta.Root), ", "), m.meta.Command)
	case session.AcceptUnlocked:
		return fmt.Sprintf("\n\n# Acceptance tests (unlocked)\nThe locked acceptance tests for %q are temporarily unlocked so a wrong test can be fixed: %s. Change only what is actually wrong, then tell the user to run /accept lock again.", m.meta.Request, strings.Join(relFiles(m.meta.Files, m.meta.Root), ", "))
	}
	return ""
}

func relFiles(files []string, root string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = textfmt.RelTo(root, f)
	}
	return out
}
