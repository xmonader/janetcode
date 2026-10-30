package agent

import (
	"errors"
	"fmt"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// ErrNoSuchMessage is returned when the n-th typed message cannot be
// found in the live conversation (a compaction folded it away).
var ErrNoSuchMessage = errors.New("that message is no longer in the conversation (it was compacted away)")

// RewindBeforeUser cuts the conversation back to just before the n-th
// message the person typed (1-based), so "Edit & resend" replaces it
// rather than appending after it. The files are not touched; restoring
// them is "/undo turn". Like /undo turn the log stays append-only: the
// dropped messages are shadowed by a replacement note.
func (a *Agent) RewindBeforeUser(n int) (dropped int, err error) {
	if n < 1 {
		return 0, ErrNoSuchMessage
	}
	keep, count := -1, 0
	for i, m := range a.Messages {
		if session.RealUser(m) {
			if count++; count == n {
				keep = i
				break
			}
		}
	}
	if keep < 0 {
		return 0, ErrNoSuchMessage
	}
	dropped = len(a.Messages) - keep
	note := llm.HarnessMessage(llm.RoleUser, fmt.Sprintf(
		"[harness] The user edited one of their earlier messages and resent it, so this conversation was rewound to before it: %d messages after that point are no longer part of your context. Whatever they said about files may be out of date; read a file again before relying on it.", dropped))
	if !a.rewindThroughLog(keep, note) {
		a.setMessages(append(a.Messages[:keep:keep], note))
	}
	a.undoRemoved = dropped
	a.stepN = 0
	a.contextManager.rewritten()
	a.EstimateTokens()
	return dropped, nil
}

// MessageCount is how many messages the live conversation holds.
func (a *Agent) MessageCount() int {
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	return len(a.Messages)
}
