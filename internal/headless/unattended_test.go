package headless

import (
	"io"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

// The -p approver says when it has no terminal, so the audit can record a
// refusal as the run's own and not a person's.
func TestStdinApproverSaysWhenNoOneCanAnswer(t *testing.T) {
	a := newStdinApprover(nil, io.Discard)
	defer a.Close()
	var ap agent.Approver = a
	u, ok := ap.(agent.Unattended)
	if !ok || !u.Unattended() {
		t.Fatal("an approver with no terminal should be unattended")
	}
	a.interactive = func() bool { return true }
	if u.Unattended() {
		t.Fatal("an approver with a terminal is attended")
	}
}
