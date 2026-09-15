package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// "Acceptance tests you approve first, that the model can't weaken."
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
// State lives on session.AcceptMeta (Agent.Accept), persisted through
// OnAcceptChange the same way Cron persists armed loops (see cron.go's
// OnChange): the front end that built the agent wires the callback to
// session.Session.SetAccept, so this package never needs its own
// *session.Session reference.
//
// Accept is shared between goroutines: a front-end changes it (/accept
// approve, lock, off) while a turn may be running tools that consult it.
// acceptMu guards the pointer and everything under it. It is only ever
// held for bookkeeping and file hashing — never across emit, a model call,
// or the gate's test run — so it cannot stall an approval prompt.
//
// The mode is spread over: accept_cmd.go (the /accept commands),
// accept_lock.go (refusing and restoring changes to locked files),
// accept_gate.go (the ACCEPT-VERIFY line and the test gate at turn end),
// testcmd.go (guessing a test command), and this file (the phase and the
// system prompt text).

// persistAccept tells the front end accept state changed, so it can keep
// the session record in step (see OnAcceptChange). Called with acceptMu
// held; the callback gets a copy, since the session keeps what it is
// given and this agent goes on changing its own.
func (a *Agent) persistAccept() {
	if a.OnAcceptChange != nil {
		a.OnAcceptChange(a.Accept.Clone())
	}
}

// acceptPhase is the current phase, and false when accept mode is off.
func (a *Agent) acceptPhase() (session.AcceptPhase, bool) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "", false
	}
	return a.Accept.Phase, true
}

func (a *Agent) verifyCommands() []string {
	gate, _ := a.VerifyGate()
	if gate == nil {
		return nil
	}
	return gate.Config.Commands
}

// acceptSystemText is appended to the system prompt while accept mode is
// on, so the model is told the rules without paying for them once the
// mode ends (see systemPrompt in context.go).
func (a *Agent) acceptSystemText() string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return ""
	}
	switch a.Accept.Phase {
	case session.AcceptWriting:
		return fmt.Sprintf("\n\n# Acceptance tests (writing)\nThe user wants acceptance tests for this request, approved before any implementation:\n\n%s\n\nWrite only test files: ones that fail against the code as it stands and would pass once the request is done. No implementation changes — every other mutating tool is refused while this phase lasts, except read-only shell commands and running the tests. When the tests are ready, end your final message with one line of the exact form:\nACCEPT-VERIFY: <command that runs the tests you wrote>\nThe user reviews the tests as a diff and runs /accept approve; only then does work on the implementation start.", a.Accept.Request)
	case session.AcceptProposed:
		return fmt.Sprintf("\n\n# Acceptance tests (proposed)\nAcceptance tests for %q are written and awaiting the user's /accept approve (proposed verification command: %s). Do not start implementing until it is approved.", a.Accept.Request, a.Accept.Proposed)
	case session.AcceptLocked:
		return fmt.Sprintf("\n\n# Acceptance tests (locked)\nThe acceptance tests for %q are approved and locked: %s. Any edit_file, write_file or bash command that would change them is refused — implement the code so they pass instead. They also gate this session: a turn is not done until `%s` passes. If a test is actually wrong, tell the user to run /accept unlock.", a.Accept.Request, strings.Join(relFiles(a.Accept.Files, a.Accept.Root), ", "), a.Accept.Command)
	case session.AcceptUnlocked:
		return fmt.Sprintf("\n\n# Acceptance tests (unlocked)\nThe locked acceptance tests for %q are temporarily unlocked so a wrong test can be fixed: %s. Change only what is actually wrong, then tell the user to run /accept lock again.", a.Accept.Request, strings.Join(relFiles(a.Accept.Files, a.Accept.Root), ", "))
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
