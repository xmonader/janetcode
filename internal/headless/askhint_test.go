package headless

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

// A call an ask rule forces is refused without a terminal, and the note
// does not tell a --yolo run to use --yolo.
func TestNonInteractiveRefusalNamesTheAskRule(t *testing.T) {
	var out bytes.Buffer
	a := newStdinApprover(nil, &out)
	a.interactive = func() bool { return false }
	call := agent.ToolCall{Name: "read_file", Input: []byte(`{"path":".env"}`), Warning: "asks because of rule *(touches:**/.env)"}
	if d, _, _ := a.ApproveWithReason(context.Background(), call); d != agent.Deny {
		t.Fatalf("decision = %v", d)
	}
	if s := out.String(); strings.Contains(s, "use --yolo") || !strings.Contains(s, "*(touches:**/.env)") {
		t.Errorf("note = %q", s)
	}
	out.Reset()
	a.ApproveWithReason(context.Background(), agent.ToolCall{Name: "bash", Input: []byte(`{}`)})
	if !strings.Contains(out.String(), "use --yolo") {
		t.Errorf("an ordinary refusal lost its hint: %q", out.String())
	}
}
