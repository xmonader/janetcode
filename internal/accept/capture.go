package accept

import (
	"fmt"
	"github.com/xmonader/janetcode/internal/textfmt"
	"regexp"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// verifyLine is the structured final line a test-writing turn ends
// with to propose the command that runs the tests it wrote, e.g.
// "ACCEPT-VERIFY: go test ./internal/foo/...". This was chosen over a
// dedicated tool: the model already ends every turn with a prose answer,
// so one line in a fixed shape gets the value back with no new schema,
// no extra round trip, and nothing for a front-end to render specially.
var verifyLine = regexp.MustCompile(`(?m)^\s*ACCEPT-VERIFY:\s*(.+?)\s*$`)

// Capture handles the end of a writing-phase turn: it looks for the
// model's stated verification command and moves the mode to "proposed"
// for the user to review, nudges once for tests or a command that are
// missing, and otherwise falls back to an inferred command rather than
// nudging forever.
//
// It reports the notice to show and the harness text to send the model
// back with ("" to end the turn). nudged is the caller's own once-per-turn
// flag; verifyCommands are the project's checks, for the inferred
// fallback.
func (m *Mode) Capture(text string, nudged *bool, verifyCommands []string) (notice, harness string) {
	if m == nil {
		return "", ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Phase != session.AcceptWriting {
		return "", ""
	}
	if len(m.meta.Files) == 0 {
		if *nudged {
			return "", "" // said once; a model that will not write tests is not made to by looping
		}
		*nudged = true
		return "accept: no test files written yet", "[accept] You have not written any test files yet. Write acceptance tests for the request, touching only test files, then end your message with a line \"ACCEPT-VERIFY: <command to run them>\"."
	}
	if sub := verifyLine.FindStringSubmatch(text); sub != nil {
		if cmd := verify.CleanCommand(sub[1]); cmd != "" {
			m.meta.Proposed = cmd
			m.meta.Phase = session.AcceptProposed
			m.persist()
			return fmt.Sprintf("accept: %s proposed · verifies with: %s · review, then /accept approve", textfmt.Plural(len(m.meta.Files), "test file"), m.meta.Proposed), ""
		}
		// Captured something, but it is not usable as one literal shell
		// command as stated — empty once any wrapping backticks/quotes
		// are stripped, or it spans more than one line. Running it
		// as-is is exactly how a backtick-wrapped command turned into a
		// command-substitution bug (see verify.CleanCommand), so this
		// falls back the same way a missing command does rather than
		// storing something unsafe to run.
		if *nudged {
			m.meta.Proposed = InferTestCommand(m.meta.Files, m.meta.Root, verifyCommands)
			m.meta.Phase = session.AcceptProposed
			m.persist()
			return "accept: the stated ACCEPT-VERIFY command was not usable as a single command; using an inferred one — check it before approving: " + m.meta.Proposed, ""
		}
		*nudged = true
		return "accept: ACCEPT-VERIFY command was not usable", "[accept] The ACCEPT-VERIFY line must name exactly one single-line shell command, with nothing else on that line — no wrapping quotes or backticks needed. State it again."
	}
	if *nudged {
		m.meta.Proposed = InferTestCommand(m.meta.Files, m.meta.Root, verifyCommands)
		m.meta.Phase = session.AcceptProposed
		m.persist()
		return "accept: no command was stated; using an inferred one — check it before approving: " + m.meta.Proposed, ""
	}
	*nudged = true
	return "accept: no verification command stated", "[accept] End your message with a line of the exact form \"ACCEPT-VERIFY: <command>\" naming how to run the tests you just wrote."
}

// GateTarget is the command the locked tests are run with and the
// directory to run it in, and false when no gate applies (the mode is
// off, or is not in a locked phase, or has no command).
func (m *Mode) GateTarget() (command, root string, ok bool) {
	if m == nil {
		return "", "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Command == "" {
		return "", "", false
	}
	if m.meta.Phase != session.AcceptLocked && m.meta.Phase != session.AcceptUnlocked {
		return "", "", false
	}
	return m.meta.Command, m.meta.Root, true
}

// CheckIntegrity runs the integrity check and, when a locked file no
// longer matches, records the reason on the state and reports it.
func (m *Mode) CheckIntegrity() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bad := m.integrityCheckLocked()
	if len(bad) == 0 {
		return ""
	}
	reason := strings.Join(bad, "; ")
	m.meta.Tamper = reason
	m.persist()
	return reason
}

// RecordGate stores the result of one gate run, unless an /accept off or
// a new /accept <request> replaced the state while the tests ran.
func (m *Mode) RecordGate(meta *session.AcceptMeta, passed bool, summary string) {
	if m == nil || meta == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Request != meta.Request || m.meta.Phase != meta.Phase {
		return
	}
	m.meta.Gate = &session.AcceptGateResult{At: time.Now(), Passed: passed, Summary: summary}
	m.persist()
}
