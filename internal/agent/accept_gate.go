package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// acceptVerifyLine is the structured final line a test-writing turn ends
// with to propose the command that runs the tests it wrote, e.g.
// "ACCEPT-VERIFY: go test ./internal/foo/...". This was chosen over a
// dedicated tool: the model already ends every turn with a prose answer,
// so one line in a fixed shape gets the value back with no new schema,
// no extra round trip, and nothing for a front-end to render specially.
var acceptVerifyLine = regexp.MustCompile(`(?m)^\s*ACCEPT-VERIFY:\s*(.+?)\s*$`)

// acceptCapture handles the end of a writing-phase turn: it looks for
// the model's stated verification command and moves the mode to
// "proposed" for the user to review, nudges once for tests or a command
// that are missing, and otherwise falls back to an inferred command
// rather than nudging forever. It reports whether the turn should
// continue for another model call.
func (a *Agent) acceptCapture(text string, nudged *bool, emit func(Event)) bool {
	notice, harness := a.acceptCaptureLocked(text, nudged)
	if notice != "" {
		emit(Event{Kind: EvNotice, Text: notice})
	}
	if harness == "" {
		return false
	}
	a.appendHarnessText(harness)
	return true
}

// acceptCaptureLocked is acceptCapture's decision, made under acceptMu: the
// notice to show, and the harness text to send the model back with ("" to
// end the turn).
func (a *Agent) acceptCaptureLocked(text string, nudged *bool) (notice, harness string) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptWriting {
		return "", ""
	}
	if len(a.Accept.Files) == 0 {
		if *nudged {
			return "", "" // said once; a model that will not write tests is not made to by looping
		}
		*nudged = true
		return "accept: no test files written yet", "[accept] You have not written any test files yet. Write acceptance tests for the request, touching only test files, then end your message with a line \"ACCEPT-VERIFY: <command to run them>\"."
	}
	if m := acceptVerifyLine.FindStringSubmatch(text); m != nil {
		if cmd := cleanVerifyCommand(m[1]); cmd != "" {
			a.Accept.Proposed = cmd
			a.Accept.Phase = session.AcceptProposed
			a.persistAccept()
			return fmt.Sprintf("accept: %d test file(s) proposed · verifies with: %s · review, then /accept approve", len(a.Accept.Files), a.Accept.Proposed), ""
		}
		// Captured something, but it is not usable as one literal shell
		// command as stated — empty once any wrapping backticks/quotes
		// are stripped, or it spans more than one line. Running it
		// as-is is exactly how a backtick-wrapped command turned into a
		// command-substitution bug (see cleanVerifyCommand), so this
		// falls back the same way a missing command does rather than
		// storing something unsafe to run.
		if *nudged {
			a.Accept.Proposed = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
			a.Accept.Phase = session.AcceptProposed
			a.persistAccept()
			return "accept: the stated ACCEPT-VERIFY command was not usable as a single command; using an inferred one — check it before approving: " + a.Accept.Proposed, ""
		}
		*nudged = true
		return "accept: ACCEPT-VERIFY command was not usable", "[accept] The ACCEPT-VERIFY line must name exactly one single-line shell command, with nothing else on that line — no wrapping quotes or backticks needed. State it again."
	}
	if *nudged {
		a.Accept.Proposed = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
		a.Accept.Phase = session.AcceptProposed
		a.persistAccept()
		return "accept: no command was stated; using an inferred one — check it before approving: " + a.Accept.Proposed, ""
	}
	*nudged = true
	return "accept: no verification command stated", "[accept] End your message with a line of the exact form \"ACCEPT-VERIFY: <command>\" naming how to run the tests you just wrote."
}

// acceptVerifyGate is the locked tests' run command as an extra
// verification gate for this session only (not written to
// .janetcode/verify.json, and not applied to any other session): the
// turn is not done until it passes, on top of whatever the project's own
// verify.json already requires. Integrity is checked before every run —
// a tampered file fails the gate outright, without even running the
// command, since a passing result over changed tests would prove
// nothing.
func (a *Agent) acceptVerifyGate(ctx context.Context, g *gateState, emit func(Event)) bool {
	// The state is read under the lock and the command run without it:
	// the tests can take minutes, and a front-end must still be able to
	// run /accept off or /accept status meanwhile.
	a.acceptMu.Lock()
	acc := a.Accept
	if acc == nil || acc.Command == "" || (acc.Phase != session.AcceptLocked && acc.Phase != session.AcceptUnlocked) {
		a.acceptMu.Unlock()
		return false
	}
	if a.mutations == 0 || a.mutations == g.seen {
		a.acceptMu.Unlock()
		return false
	}
	g.seen = a.mutations
	command, root := acc.Command, acc.Root
	var reason string
	if bad := a.acceptIntegrityCheck(); len(bad) > 0 {
		reason = strings.Join(bad, "; ")
		acc.Tamper = reason
		a.persistAccept()
	}
	a.acceptMu.Unlock()
	if reason != "" {
		msg := "accept: integrity check failed — " + reason
		emit(Event{Kind: EvNotice, Text: msg, IsError: true})
		g.rounds++
		if g.rounds > verify.MaxRounds {
			emit(Event{Kind: EvNotice, Text: "accept: giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
			return false
		}
		a.appendHarnessText("[accept] " + msg + ". A locked acceptance test changed outside a tool call, which is reported instead of trusted. Restore the locked test files exactly as approved, or tell the user the tests need /accept unlock.")
		return true
	}
	emit(Event{Kind: EvNotice, Text: "accept: verifying locked tests · " + command})
	r := &verify.Runner{Config: verify.Config{Commands: []string{command}}, Cwd: root}
	res := r.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.acceptMu.Lock()
	// Only the mode this gate ran for records its result: an /accept off
	// or a new /accept <request> while the tests ran has replaced it.
	if a.Accept == acc {
		acc.Gate = &session.AcceptGateResult{At: time.Now(), Passed: res.OK(), Summary: res.Summary()}
		a.persistAccept()
	}
	a.acceptMu.Unlock()
	if res.OK() {
		emit(Event{Kind: EvNotice, Text: "accept: " + res.Summary()})
		return false
	}
	// Exit 126/127 means the command itself could not be run at all — not
	// found, or not executable — not that a test failed. Sending the
	// model back to "fix the code" for that burns whole rounds (a QA run
	// saw ~7 minutes of retries) on something no code change can fix; the
	// gate gives up on the first one and points at the one thing that
	// actually helps.
	if res.Exit == 126 || res.Exit == 127 {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("accept: %s · the command itself did not run (exit %d) — this is not a failing test; fix it with /accept command <cmd> instead of retrying", res.Summary(), res.Exit), IsError: true})
		return false
	}
	g.rounds++
	if g.rounds > verify.MaxRounds {
		emit(Event{Kind: EvNotice, Text: "accept: " + res.Summary() + " · giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
		return false
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("accept: %s · sending the model back to fix it (%d/%d)", res.Summary(), g.rounds, verify.MaxRounds)})
	a.appendHarnessText("[accept] The locked acceptance tests are this session's gate:\n\n" + res.Feedback())
	return true
}
