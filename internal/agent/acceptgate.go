package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xmonader/janetcode/internal/accept"
	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// The /accept mode itself lives in package accept: the state, the phase
// rules, the lock over the approved test files, and the /accept commands.
// What stays here is the part that belongs to the turn — reading the
// model's ACCEPT-VERIFY line as a turn ends, and running the locked
// tests as an extra gate — plus the forwarding methods the front-ends
// call, which are unchanged from when the whole mode lived in this
// package.

// Locks is the part of accept mode the tool path consults: what a mutating
// call is refused for, and what has to be put back after one ran anyway.
// *accept.Mode is the implementation; the interface is what exec.go and
// modes.go depend on, so the tool path can be exercised against a stub
// that denies on demand without setting up a locked test suite.
type Locks interface {
	// Denial reports why a mutating call must not run because it touches
	// a locked acceptance test, or "" when it may proceed.
	Denial(t tool.Tool, input json.RawMessage) string
	// WritingDenial reports why a mutating call must not run during the
	// writing or proposed phase, or "" when it may proceed.
	WritingDenial(t tool.Tool, input json.RawMessage) string
	// RestoreTampered puts back any locked file whose content no longer
	// matches what was approved, and reports what it did, or "".
	RestoreTampered(toolName string) string
	// TrackEdit records a test file the writing phase wrote, or refreshes
	// a locked file's hash after an edit made while unlocked.
	TrackEdit(t tool.Tool, input json.RawMessage)
}

// AcceptArgs is what "/accept ..." asks for; see accept.Args.
type AcceptArgs = accept.Args

// ParseAcceptArgs parses the text after "/accept"; see accept.ParseArgs.
func ParseAcceptArgs(rest string) AcceptArgs { return accept.ParseArgs(rest) }

// SetAccept installs the /accept state a resumed session carries (nil for
// a fresh one) and the callback that persists later changes to it, the
// way OnChange does for Cron.
func (a *Agent) SetAccept(meta *session.AcceptMeta, onChange func(*session.AcceptMeta)) {
	a.accept.Store(accept.New(meta, onChange))
}

// AcceptMeta is a copy of the /accept state, or nil when the mode is off.
func (a *Agent) AcceptMeta() *session.AcceptMeta { return a.accept.Load().Meta() }

// acceptMode is the live mode, created on first use so a /accept on an
// agent nothing wired one into still works.
func (a *Agent) acceptMode() *accept.Mode {
	// A turn reads the mode while a front-end's /accept may be creating
	// it, so it is created at most once, atomically.
	if m := a.accept.Load(); m != nil {
		return m
	}
	a.accept.CompareAndSwap(nil, accept.New(nil, nil))
	return a.accept.Load()
}

// AcceptCommand runs a parsed /accept for every front-end; see
// accept.Mode.Command. Off["accept"] restores a session that has no
// accept mode: the command itself is refused, so nothing can start it.
func (a *Agent) AcceptCommand(args AcceptArgs, root string) (cmdresult.Result, error) {
	if a.Off["accept"] {
		return cmdresult.Result{}, fmt.Errorf("accept is off")
	}
	return a.acceptMode().Command(args, root, a.verifyCommands())
}

// AcceptStatus is the report for a bare /accept.
func (a *Agent) AcceptStatus() string { return a.acceptMode().Status() }

// verifyCommands are the project's own configured checks, which accept
// mode falls back to when it has to infer a test command.
func (a *Agent) verifyCommands() []string {
	gate, _ := a.VerifyGate()
	if gate == nil {
		return nil
	}
	return gate.Config.Commands
}

// acceptPhase is the current phase, and false when accept mode is off.
func (a *Agent) acceptPhase() (session.AcceptPhase, bool) { return a.accept.Load().Phase() }

// acceptRestoreTampered restores any tampered locked file and tells both
// the user and the tool call that caused it (see accept.Mode.RestoreTampered).
func (a *Agent) acceptRestoreTampered(toolName string, emit func(Event)) string {
	msg := a.accept.Load().RestoreTampered(toolName)
	if msg != "" {
		emit(Event{Kind: EvNotice, Text: "accept: " + msg, IsError: true})
	}
	return msg
}

// acceptCapture handles the end of a writing-phase turn (see
// accept.Mode.Capture) and reports whether the turn should continue for
// another model call.
func (a *Agent) acceptCapture(text string, nudged *bool, emit func(Event)) bool {
	if a.Off["accept"] {
		return false
	}
	notice, harness := a.accept.Load().Capture(text, nudged, a.verifyCommands())
	if notice != "" {
		emit(Event{Kind: EvNotice, Text: notice})
	}
	if harness == "" {
		return false
	}
	a.appendHarnessText(harness)
	return true
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
	if a.Off["accept"] {
		return false
	}
	command, root, ok := a.accept.Load().GateTarget()
	if !ok {
		return false
	}
	if a.mutations == 0 || a.mutations == g.seen {
		return false
	}
	g.seen = a.mutations
	// The state is read and the command run without the mode's lock: the
	// tests can take minutes, and a front-end must still be able to run
	// /accept off or /accept status meanwhile.
	if reason := a.accept.Load().CheckIntegrity(); reason != "" {
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
	before := a.accept.Load().Meta()
	emit(Event{Kind: EvNotice, Text: "accept: verifying locked tests · " + command})
	r := &verify.Runner{Config: verify.Config{Commands: []string{command}}, Cwd: root}
	res := r.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.logGate(session.GateAccept, res)
	// Only the mode this gate ran for records its result: an /accept off
	// or a new /accept <request> while the tests ran has replaced it.
	a.accept.Load().RecordGate(before, res.OK(), res.Summary())
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

// *accept.Mode is the Locks implementation the tool path actually runs
// against; a nil one is a session that never used the mode, and every
// method on it answers "nothing to refuse".
var _ Locks = (*accept.Mode)(nil)

// locks is the accept-mode lock the tool path consults. A nil *accept.Mode
// is a working value, so this never has to create one just to be asked.
func (a *Agent) locks() Locks {
	if a.Off["accept"] {
		return (*accept.Mode)(nil)
	}
	return a.accept.Load()
}

// AcceptStart puts the session in test-writing mode for request; see
// accept.Mode.Start.
func (a *Agent) AcceptStart(request, root string) { a.acceptMode().Start(request, root) }

// AcceptOff ends accept mode outright; see accept.Mode.Off.
func (a *Agent) AcceptOff() { a.acceptMode().Off() }

// AcceptApprove locks in what the writing phase produced; see
// accept.Mode.Approve.
func (a *Agent) AcceptApprove() (string, error) {
	return a.acceptMode().Approve(a.verifyCommands())
}

// AcceptLock re-locks the files an earlier approval named; see
// accept.Mode.Lock.
func (a *Agent) AcceptLock() error { return a.acceptMode().Lock() }

// AcceptUnlock lifts the lock so a wrong test can be fixed; see
// accept.Mode.Unlock.
func (a *Agent) AcceptUnlock() error { return a.acceptMode().Unlock() }

// AcceptSetCommand replaces the verification command; see
// accept.Mode.SetCommand.
func (a *Agent) AcceptSetCommand(raw string) (string, error) {
	return a.acceptMode().SetCommand(raw)
}

// ErrAcceptAlreadyLocked is AcceptApprove's answer when the tests are
// already approved and locked.
var ErrAcceptAlreadyLocked = accept.ErrAlreadyLocked
