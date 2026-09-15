package accept

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/slash/args"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// Args is what "/accept ..." asks for, parsed once in
// ParseArgs (the same shape as agent's ParseUndoArgs) so
// the terminal and the web UI agree on what each spelling means.
type Args struct {
	// Status is a bare /accept: report where the mode stands.
	Status bool
	// Request starts the mode: write acceptance tests for this text.
	Request                    string
	Approve, Lock, Unlock, Off bool
	// SetCommand is /accept command <cmd>: replace the verify command
	// directly, when the model's stated or inferred one is wrong (see
	// Agent.AcceptSetCommand). Command holds the text after "command ",
	// still raw — SetCommand is what cleans it.
	SetCommand bool
	Command    string
}

// ParseArgs parses the text after "/accept". Anything that is not
// one of the fixed words is a request, the same way /plan's argument is
// free text unless it names approve/off.
func ParseArgs(rest string) Args {
	t, _ := args.Tokenize(rest, args.Spec{Rest: []string{"command"}})
	if cmd, ok := t.Flags["command"]; ok && len(t.Positional) == 0 {
		return Args{SetCommand: true, Command: cmd}
	}
	if len(t.Flags) == 0 && len(t.Positional) <= 1 {
		switch strings.Join(t.Positional, "") {
		case "":
			return Args{Status: true}
		case "approve":
			return Args{Approve: true}
		case "lock":
			return Args{Lock: true}
		case "unlock":
			return Args{Unlock: true}
		case "off", "cancel", "exit":
			return Args{Off: true}
		}
	}
	return Args{Request: strings.TrimSpace(rest)}
}

// Command runs a parsed /accept for every front-end: it starts
// test-writing mode (Result.Handover is the request, to submit as the
// next turn), reports status, or resolves approve/lock/unlock/command/off.
// root is the project directory accept mode's paths are relative to;
// verifyCommands are the project's own checks, which Approve falls back
// to when it has to infer a test command.
func (m *Mode) Command(args Args, root string, verifyCommands []string) (cmdresult.Result, error) {
	note := func(s string) (cmdresult.Result, error) { return cmdresult.Result{Notes: []string{s}}, nil }
	switch {
	case args.Status:
		return cmdresult.Result{Sheet: m.Status(), SheetTitle: "accept", Plain: true}, nil
	case args.Approve:
		cmd, err := m.Approve(verifyCommands)
		if errors.Is(err, ErrAlreadyLocked) {
			return cmdresult.Result{Sheet: "accept: already locked · verifies with: " + cmd, Plain: true}, nil
		}
		if err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		// In the transcript, not a status line that the next frame
		// replaces: the command is what the user is agreeing to be gated by.
		return cmdresult.Result{Sheet: m.approvedSheet(cmd), Plain: true}, nil
	case args.Lock:
		if err := m.Lock(); err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: locked")
	case args.Unlock:
		if err := m.Unlock(); err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: unlocked · edits to the test files are allowed until /accept lock")
	case args.SetCommand:
		cmd, err := m.SetCommand(args.Command)
		if err != nil {
			return cmdresult.Result{}, fmt.Errorf("accept: %w", err)
		}
		return note("accept: command set to " + cmd)
	case args.Off:
		m.Off()
		return note("accept: off")
	default:
		m.Start(args.Request, root)
		return cmdresult.Result{Status: "accept: writing tests for " + args.Request, Handover: args.Request}, nil
	}
}

// Start puts the session in test-writing mode for request: until
// the model proposes a command and the user approves it, every mutating
// tool call is refused except a write_file/edit_file on a test file (see
// WritingDenial).
func (m *Mode) Start(request, root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meta = &session.AcceptMeta{Phase: session.AcceptWriting, Request: strings.TrimSpace(request), Root: root}
	m.persist()
}

// Off ends accept mode outright: whatever was locked is unlocked
// and the gate stops running.
func (m *Mode) Off() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meta = nil
	m.persist()
}

// ErrAlreadyLocked is Approve's answer when the tests are
// already approved and locked; the command it returns is the one in effect.
var ErrAlreadyLocked = errors.New("already locked")

// approvedSheet is what /accept approve shows: the files locked and
// the command that now gates the session, for the user to confirm.
func (m *Mode) approvedSheet(cmd string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	b.WriteString("accept: locked\n")
	if m.meta != nil {
		for _, f := range relFiles(m.meta.Files, m.meta.Root) {
			b.WriteString("  " + f + "\n")
		}
	}
	fmt.Fprintf(&b, "verifies with: %s\n/accept command <cmd> changes it; /accept unlock lets a wrong test be fixed", cmd)
	return b.String()
}

// Approve locks in what the writing phase produced: it requires at
// least one test file and a verification command (the model's own, or
// one inferred as a fallback — see InferTestCommand), records every
// file's hash, and arms the gate. It returns the command now in effect.
func (m *Mode) Approve(verifyCommands []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta != nil && m.meta.Phase == session.AcceptLocked {
		return m.meta.Command, ErrAlreadyLocked
	}
	if m.meta != nil && m.meta.Phase == session.AcceptUnlocked {
		return "", fmt.Errorf("already approved and unlocked for a test fix: /accept lock locks it again")
	}
	if m.meta == nil || m.meta.Phase != session.AcceptProposed {
		return "", fmt.Errorf("nothing proposed yet: /accept <request> writes tests first")
	}
	if len(m.meta.Files) == 0 {
		return "", fmt.Errorf("no test files were written")
	}
	cmd := verify.CleanCommand(m.meta.Proposed)
	if cmd == "" {
		cmd = InferTestCommand(m.meta.Files, m.meta.Root, verifyCommands)
	}
	if cmd == "" {
		return "", fmt.Errorf("no verification command: the model did not state one and none could be inferred; write one yourself with /accept lock after setting it, or /accept off and try again")
	}
	m.meta.Command = cmd
	if err := m.rehash(); err != nil {
		return "", err
	}
	m.captureContents()
	m.meta.Phase = session.AcceptLocked
	m.meta.Proposed = ""
	m.meta.Tamper = ""
	m.persist()
	return cmd, nil
}

// Lock re-locks the files an earlier approval named, hashing their
// current content, after /accept unlock let the model fix a wrong test.
func (m *Mode) Lock() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || (m.meta.Phase != session.AcceptUnlocked && m.meta.Phase != session.AcceptLocked) {
		return fmt.Errorf("nothing approved yet to lock")
	}
	if err := m.rehash(); err != nil {
		return err
	}
	m.captureContents()
	m.meta.Phase = session.AcceptLocked
	m.meta.Tamper = ""
	m.persist()
	return nil
}

// SetCommand replaces the verification command directly — for
// /accept command <cmd>, when the model's stated or inferred command
// turns out wrong (a bad ACCEPT-VERIFY line, or a gate that failed
// because the command itself would not run at all — see
// acceptVerifyGate's exit-126/127 handling). It does not touch the
// locked files or their hashes/content, only which command gates the
// session and gets approved.
func (m *Mode) SetCommand(raw string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return "", fmt.Errorf("nothing started yet")
	}
	cmd := verify.CleanCommand(raw)
	if cmd == "" {
		return "", fmt.Errorf("command is empty, or is not a single line")
	}
	switch m.meta.Phase {
	case session.AcceptProposed:
		m.meta.Proposed = cmd
	case session.AcceptLocked, session.AcceptUnlocked:
		m.meta.Command = cmd
	default:
		return "", fmt.Errorf("nothing proposed or locked yet to set a command for")
	}
	m.persist()
	return cmd, nil
}

// Unlock lifts the lock so the model may edit the test files — a
// test turned out wrong, and fixing it is a legitimate edit the user is
// asking for by name, not one the model talked its way into.
func (m *Mode) Unlock() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Phase != session.AcceptLocked {
		return fmt.Errorf("tests are not locked")
	}
	m.meta.Phase = session.AcceptUnlocked
	m.persist()
	return nil
}

// Status is the report for a bare /accept: the request, the locked
// files, whether their hashes still match, and the last gate result.
func (m *Mode) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return "accept mode is off: /accept <request> writes acceptance tests for it first"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "accept: %s\nrequest: %s\n", m.meta.Phase, m.meta.Request)
	switch m.meta.Phase {
	case session.AcceptWriting:
		b.WriteString("writing acceptance tests — only test files may be written; the model ends with an ACCEPT-VERIFY line when done\n")
	case session.AcceptProposed:
		fmt.Fprintf(&b, "proposed command: %q\nreview the files below (a front-end /diff shows each one), then /accept approve — or /accept command <cmd> to correct it first\n", m.meta.Proposed)
	}
	if len(m.meta.Files) > 0 {
		b.WriteString("files:\n")
		for _, f := range m.meta.Files {
			b.WriteString("  " + textfmt.RelTo(m.meta.Root, f) + "\n")
		}
	}
	if m.meta.Command != "" {
		// %q rather than %s: the exact command as it will be run, quotes
		// and any stray wrapping visible rather than swallowed, so a bad
		// one (see verify.CleanCommand) is obvious before it burns a gate
		// round.
		fmt.Fprintf(&b, "command: %q\n", m.meta.Command)
	}
	if m.meta.Phase == session.AcceptLocked || m.meta.Phase == session.AcceptUnlocked {
		if bad := m.integrityCheckLocked(); len(bad) > 0 {
			b.WriteString("hashes: TAMPERED — " + strings.Join(bad, "; ") + "\n")
		} else {
			b.WriteString("hashes: intact\n")
		}
	}
	if m.meta.Gate != nil {
		state := "failed"
		if m.meta.Gate.Passed {
			state = "passed"
		}
		fmt.Fprintf(&b, "last gate: %s at %s — %s\n", state, m.meta.Gate.At.Format(time.Kitchen), m.meta.Gate.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}
