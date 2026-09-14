package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	rest = strings.TrimSpace(rest)
	switch rest {
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
	if arg, ok := strings.CutPrefix(rest, "command"); ok && (arg == "" || arg[0] == ' ') {
		return AcceptArgs{SetCommand: true, Command: strings.TrimSpace(arg)}
	}
	return AcceptArgs{Request: rest}
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

// AcceptApprove locks in what the writing phase produced: it requires at
// least one test file and a verification command (the model's own, or
// one inferred as a fallback — see InferTestCommand), records every
// file's hash, and arms the gate. It returns the command now in effect.
func (a *Agent) AcceptApprove() (string, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptProposed {
		return "", fmt.Errorf("nothing proposed yet: /accept <request> writes tests first")
	}
	if len(a.Accept.Files) == 0 {
		return "", fmt.Errorf("no test files were written")
	}
	cmd := cleanVerifyCommand(a.Accept.Proposed)
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
		return fmt.Errorf("accept: nothing approved yet to lock")
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
		return "", fmt.Errorf("accept: nothing started yet")
	}
	cmd := cleanVerifyCommand(raw)
	if cmd == "" {
		return "", fmt.Errorf("accept: command is empty, or is not a single line")
	}
	switch a.Accept.Phase {
	case session.AcceptProposed:
		a.Accept.Proposed = cmd
	case session.AcceptLocked, session.AcceptUnlocked:
		a.Accept.Command = cmd
	default:
		return "", fmt.Errorf("accept: nothing proposed or locked yet to set a command for")
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
		return fmt.Errorf("accept: tests are not locked")
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
		// one (see cleanVerifyCommand) is obvious before it burns a gate
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

// cleanVerifyCommand takes the raw text after "ACCEPT-VERIFY:" (or a
// user's /accept command argument) down to the one literal shell command
// it names, or "" if it cannot be made into one. It exists because a
// model likes to wrap the command in backticks or quotes — and a stated
// command wrapped in backticks, like `cd pkg && go test -run TestSlug`
// run literally does not run go test at all: the shell performs command substitution first,
// runs the test suite to build a new command line out of its stdout,
// then tries to execute that output's first line as a command — which is
// how a QA run's gate failed with exit 127 and "===: command not found"
// on a command that worked fine typed by hand. Only the text on the line
// itself, trimmed of that wrapping, is ever stored; anything left
// spanning more than one line, or empty once unwrapped, is rejected
// rather than run.
func cleanVerifyCommand(s string) string {
	s = strings.TrimSpace(s)
	for {
		trimmed := stripMatchingWrap(s)
		if trimmed == s {
			break
		}
		s = strings.TrimSpace(trimmed)
	}
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	return s
}

// stripMatchingWrap removes one layer of wrapping that spans the whole
// string: a triple or single backtick pair, or a matching pair of
// double or single quotes.
func stripMatchingWrap(s string) string {
	for _, wrap := range []string{"```", "`", `"`, "'"} {
		if len(s) >= 2*len(wrap) && strings.HasPrefix(s, wrap) && strings.HasSuffix(s, wrap) {
			return s[len(wrap) : len(s)-len(wrap)]
		}
	}
	return s
}
