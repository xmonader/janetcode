package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// AcceptArgs is what "/accept ..." asks for, parsed once in
// ParseAcceptArgs (the same shape as undo_intent.go's ParseUndoArgs) so
// the terminal and the web UI agree on what each spelling means.
type AcceptArgs struct {
	// Status is a bare /accept: report where the mode stands.
	Status bool
	// Request starts the mode: write acceptance tests for this text.
	Request                    string
	Approve, Lock, Unlock, Off bool
	// SetCommand is /accept command <cmd>: replace the verify command
	// directly, when the model's stated or inferred one is wrong (see
	// Agent.AcceptSetCommand). Command holds the text after "command ",
	// still raw — AcceptSetCommand is what cleans it.
	SetCommand bool
	Command    string
}

// ParseAcceptArgs parses the text after "/accept". Anything that is not
// one of the fixed words is a request, the same way /plan's argument is
// free text unless it names approve/off.
func ParseAcceptArgs(rest string) AcceptArgs {
	t, _ := slash.Tokenize(rest, slash.ArgSpec{Rest: []string{"command"}})
	if cmd, ok := t.Flags["command"]; ok && len(t.Positional) == 0 {
		return AcceptArgs{SetCommand: true, Command: cmd}
	}
	if len(t.Flags) == 0 && len(t.Positional) <= 1 {
		switch strings.Join(t.Positional, "") {
		case "":
			return AcceptArgs{Status: true}
		case "approve":
			return AcceptArgs{Approve: true}
		case "lock":
			return AcceptArgs{Lock: true}
		case "unlock":
			return AcceptArgs{Unlock: true}
		case "off", "cancel", "exit":
			return AcceptArgs{Off: true}
		}
	}
	return AcceptArgs{Request: strings.TrimSpace(rest)}
}

// AcceptCommand runs a parsed /accept for every front-end: it starts
// test-writing mode (Result.Handover is the request, to submit as the
// next turn), reports status, or resolves approve/lock/unlock/command/off.
// root is the project directory accept mode's paths are relative to.
func (a *Agent) AcceptCommand(args AcceptArgs, root string) (cmdresult.Result, error) {
	note := func(s string) (cmdresult.Result, error) { return cmdresult.Result{Notes: []string{s}}, nil }
	switch {
	case args.Status:
		return cmdresult.Result{Sheet: a.AcceptStatus(), SheetTitle: "accept", Plain: true}, nil
	case args.Approve:
		cmd, err := a.AcceptApprove()
		if errors.Is(err, ErrAcceptAlreadyLocked) {
			return cmdresult.Result{Sheet: "accept: already locked · verifies with: " + cmd, Plain: true}, nil
		}
		if err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		// In the transcript, not a status line that the next frame
		// replaces: the command is what the user is agreeing to be gated by.
		return cmdresult.Result{Sheet: a.acceptApprovedSheet(cmd), Plain: true}, nil
	case args.Lock:
		if err := a.AcceptLock(); err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: locked")
	case args.Unlock:
		if err := a.AcceptUnlock(); err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: unlocked · edits to the test files are allowed until /accept lock")
	case args.SetCommand:
		cmd, err := a.AcceptSetCommand(args.Command)
		if err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: command set to " + cmd)
	case args.Off:
		a.AcceptOff()
		return note("accept: off")
	default:
		a.AcceptStart(args.Request, root)
		return cmdresult.Result{Status: "accept: writing tests for " + args.Request, Handover: args.Request}, nil
	}
}

// AcceptStart puts the session in test-writing mode for request: until
// the model proposes a command and the user approves it, every mutating
// tool call is refused except a write_file/edit_file on a test file (see
// acceptWritingDenial).
func (a *Agent) AcceptStart(request, root string) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	a.Accept = &session.AcceptMeta{Phase: session.AcceptWriting, Request: strings.TrimSpace(request), Root: root}
	a.persistAccept()
}

// AcceptOff ends accept mode outright: whatever was locked is unlocked
// and the gate stops running.
func (a *Agent) AcceptOff() {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	a.Accept = nil
	a.persistAccept()
}

// ErrAcceptAlreadyLocked is AcceptApprove's answer when the tests are
// already approved and locked; the command it returns is the one in effect.
var ErrAcceptAlreadyLocked = errors.New("already locked")

// acceptApprovedSheet is what /accept approve shows: the files locked and
// the command that now gates the session, for the user to confirm.
func (a *Agent) acceptApprovedSheet(cmd string) string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	var b strings.Builder
	b.WriteString("accept: locked\n")
	if a.Accept != nil {
		for _, f := range relFiles(a.Accept.Files, a.Accept.Root) {
			b.WriteString("  " + f + "\n")
		}
	}
	fmt.Fprintf(&b, "verifies with: %s\n/accept command <cmd> changes it; /accept unlock lets a wrong test be fixed", cmd)
	return b.String()
}

// AcceptApprove locks in what the writing phase produced: it requires at
// least one test file and a verification command (the model's own, or
// one inferred as a fallback — see InferTestCommand), records every
// file's hash, and arms the gate. It returns the command now in effect.
func (a *Agent) AcceptApprove() (string, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept != nil && a.Accept.Phase == session.AcceptLocked {
		return a.Accept.Command, ErrAcceptAlreadyLocked
	}
	if a.Accept != nil && a.Accept.Phase == session.AcceptUnlocked {
		return "", fmt.Errorf("already approved and unlocked for a test fix: /accept lock locks it again")
	}
	if a.Accept == nil || a.Accept.Phase != session.AcceptProposed {
		return "", fmt.Errorf("nothing proposed yet: /accept <request> writes tests first")
	}
	if len(a.Accept.Files) == 0 {
		return "", fmt.Errorf("no test files were written")
	}
	cmd := verify.CleanCommand(a.Accept.Proposed)
	if cmd == "" {
		cmd = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
	}
	if cmd == "" {
		return "", fmt.Errorf("no verification command: the model did not state one and none could be inferred; write one yourself with /accept lock after setting it, or /accept off and try again")
	}
	a.Accept.Command = cmd
	if err := a.rehashAccept(); err != nil {
		return "", err
	}
	a.captureAcceptContents()
	a.Accept.Phase = session.AcceptLocked
	a.Accept.Proposed = ""
	a.Accept.Tamper = ""
	a.persistAccept()
	return cmd, nil
}

// AcceptLock re-locks the files an earlier approval named, hashing their
// current content, after /accept unlock let the model fix a wrong test.
func (a *Agent) AcceptLock() error {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || (a.Accept.Phase != session.AcceptUnlocked && a.Accept.Phase != session.AcceptLocked) {
		return fmt.Errorf("nothing approved yet to lock")
	}
	if err := a.rehashAccept(); err != nil {
		return err
	}
	a.captureAcceptContents()
	a.Accept.Phase = session.AcceptLocked
	a.Accept.Tamper = ""
	a.persistAccept()
	return nil
}

// AcceptSetCommand replaces the verification command directly — for
// /accept command <cmd>, when the model's stated or inferred command
// turns out wrong (a bad ACCEPT-VERIFY line, or a gate that failed
// because the command itself would not run at all — see
// acceptVerifyGate's exit-126/127 handling). It does not touch the
// locked files or their hashes/content, only which command gates the
// session and gets approved.
func (a *Agent) AcceptSetCommand(raw string) (string, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "", fmt.Errorf("nothing started yet")
	}
	cmd := verify.CleanCommand(raw)
	if cmd == "" {
		return "", fmt.Errorf("command is empty, or is not a single line")
	}
	switch a.Accept.Phase {
	case session.AcceptProposed:
		a.Accept.Proposed = cmd
	case session.AcceptLocked, session.AcceptUnlocked:
		a.Accept.Command = cmd
	default:
		return "", fmt.Errorf("nothing proposed or locked yet to set a command for")
	}
	a.persistAccept()
	return cmd, nil
}

// AcceptUnlock lifts the lock so the model may edit the test files — a
// test turned out wrong, and fixing it is a legitimate edit the user is
// asking for by name, not one the model talked its way into.
func (a *Agent) AcceptUnlock() error {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptLocked {
		return fmt.Errorf("tests are not locked")
	}
	a.Accept.Phase = session.AcceptUnlocked
	a.persistAccept()
	return nil
}

// AcceptStatus is the report for a bare /accept: the request, the locked
// files, whether their hashes still match, and the last gate result.
func (a *Agent) AcceptStatus() string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "accept mode is off: /accept <request> writes acceptance tests for it first"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "accept: %s\nrequest: %s\n", a.Accept.Phase, a.Accept.Request)
	switch a.Accept.Phase {
	case session.AcceptWriting:
		b.WriteString("writing acceptance tests — only test files may be written; the model ends with an ACCEPT-VERIFY line when done\n")
	case session.AcceptProposed:
		fmt.Fprintf(&b, "proposed command: %q\nreview the files below (a front-end /diff shows each one), then /accept approve — or /accept command <cmd> to correct it first\n", a.Accept.Proposed)
	}
	if len(a.Accept.Files) > 0 {
		b.WriteString("files:\n")
		for _, f := range a.Accept.Files {
			b.WriteString("  " + textfmt.RelTo(a.Accept.Root, f) + "\n")
		}
	}
	if a.Accept.Command != "" {
		// %q rather than %s: the exact command as it will be run, quotes
		// and any stray wrapping visible rather than swallowed, so a bad
		// one (see verify.CleanCommand) is obvious before it burns a gate
		// round.
		fmt.Fprintf(&b, "command: %q\n", a.Accept.Command)
	}
	if a.Accept.Phase == session.AcceptLocked || a.Accept.Phase == session.AcceptUnlocked {
		if bad := a.acceptIntegrityCheck(); len(bad) > 0 {
			b.WriteString("hashes: TAMPERED — " + strings.Join(bad, "; ") + "\n")
		} else {
			b.WriteString("hashes: intact\n")
		}
	}
	if a.Accept.Gate != nil {
		state := "failed"
		if a.Accept.Gate.Passed {
			state = "passed"
		}
		fmt.Fprintf(&b, "last gate: %s at %s — %s\n", state, a.Accept.Gate.At.Format(time.Kitchen), a.Accept.Gate.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}
