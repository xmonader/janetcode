package session

import "time"

// AcceptPhase is where an /accept session's acceptance-tests-first mode
// currently stands.
type AcceptPhase string

const (
	// AcceptWriting: the model's only job is to write acceptance tests
	// for the request; every other mutating tool is refused.
	AcceptWriting AcceptPhase = "writing"
	// AcceptProposed: tests are written and a verification command has
	// been proposed; waiting on the user to run /accept approve.
	AcceptProposed AcceptPhase = "proposed"
	// AcceptLocked: approved and locked. The test files may not be
	// touched by any tool call, and their command gates the session.
	AcceptLocked AcceptPhase = "locked"
	// AcceptUnlocked: approved, but the user lifted the lock (a test
	// turned out wrong) so the files may be edited again.
	AcceptUnlocked AcceptPhase = "unlocked"
)

// AcceptGateResult is the outcome of the last time the locked tests were
// run as this session's verification gate.
type AcceptGateResult struct {
	At      time.Time `json:"at"`
	Passed  bool      `json:"passed"`
	Summary string    `json:"summary"`
}

// AcceptMeta is the /accept "acceptance tests you approve first" state
// for one session: the request the tests answer, the files that hold
// them, their content hash as of approval (so a change outside a tool
// call is caught rather than trusted), the command that runs them, and
// the last time that command ran as this session's verification gate.
// Persisted on Meta so a resume restores the lock; see internal/agent
// for the behaviour built on top of it (Agent.Accept).
type AcceptMeta struct {
	Phase   AcceptPhase `json:"phase"`
	Request string      `json:"request"`
	// Root is the directory the file paths below and Command are
	// resolved against — the session's workspace root when /accept
	// started.
	Root string `json:"root"`
	// Files are the test files this mode has written or locked,
	// absolute paths.
	Files []string `json:"files,omitempty"`
	// Hashes is each file's sha256 as of the last approve or lock (or
	// the last edit made while unlocked), keyed by the same path.
	Hashes map[string]string `json:"hashes,omitempty"`
	// Contents is each locked file's full content as of the last approve
	// or lock, keyed by the same path as Hashes — the content guarantee
	// behind the lock: a hash alone only detects that a file was changed
	// by something other than a tool call (a bash trick, a nested script
	// call, anything the pre-execution checks missed); this is what lets
	// the harness put the file back immediately instead of only
	// reporting the tamper. A file too large to keep in full (see
	// agent.acceptMaxContentSize) has no entry here and falls back to
	// hash-only detection.
	Contents map[string][]byte `json:"contents,omitempty"`
	// Command is the approved verification command; empty until
	// AcceptProposed becomes AcceptLocked.
	Command string `json:"command,omitempty"`
	// Proposed is the command the model stated (or the harness
	// inferred) at the end of the writing phase, before the user
	// approved it.
	Proposed string            `json:"proposed,omitempty"`
	Gate     *AcceptGateResult `json:"gate,omitempty"`
	// Tamper is set when a hash check finds a locked file changed
	// outside a tool call; cleared on the next approve or lock.
	Tamper string `json:"tamper,omitempty"`
}

// Clone is a deep copy, so a snapshot handed to the session file cannot be
// changed under it by the agent that keeps mutating its own copy.
func (m *AcceptMeta) Clone() *AcceptMeta {
	if m == nil {
		return nil
	}
	c := *m
	c.Files = append([]string(nil), m.Files...)
	if m.Hashes != nil {
		c.Hashes = make(map[string]string, len(m.Hashes))
		for k, v := range m.Hashes {
			c.Hashes[k] = v
		}
	}
	if m.Contents != nil {
		c.Contents = make(map[string][]byte, len(m.Contents))
		for k, v := range m.Contents {
			c.Contents[k] = v // the bytes are never written in place
		}
	}
	if m.Gate != nil {
		g := *m.Gate
		c.Gate = &g
	}
	return &c
}

// SetAccept stores accept mode's state and rewrites metadata; nil clears
// it (see Agent.AcceptOff).
func (s *Session) SetAccept(a *AcceptMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Meta.Accept = a
	return s.rewriteLocked()
}
