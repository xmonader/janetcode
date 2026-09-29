package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
)

// Every call that could change something leaves a permission record
// saying what let it run or refused it, and every file it changed leaves
// a record with the content digests — both on the audited session log.
func TestPermissionAndFileChangeAreRecorded(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"b.txt","content":"beta\n"}`),
		toolCall("c2", "write_file", `{"path":"a.txt","content":"changed\n"}`),
		toolCall("c3", "read_file", `{"path":"a.txt"}`),
		text("done"),
	}}
	var asked int
	a, root := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		asked++
		return Deny, nil
	}))
	a.AddRule(perm.Allowed, perm.Rule{Tool: "write_file", Pattern: "b.txt"})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "write two files", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	var perms, changes []session.Event
	for _, e := range sess.Events() {
		switch e.Type {
		case session.TypePermission:
			perms = append(perms, e)
		case session.TypeFileChange:
			changes = append(changes, e)
		}
	}
	if len(perms) != 2 {
		t.Fatalf("permission records = %+v, want one per mutating call and none for the read", perms)
	}
	if perms[0].CallID != "c1" || perms[0].Denied || !strings.HasPrefix(perms[0].Reason, "rule write_file") {
		t.Errorf("allowed by rule: %+v", perms[0])
	}
	if perms[1].CallID != "c2" || !perms[1].Denied || perms[1].Reason != "the user: deny" || asked != 1 {
		t.Errorf("refused by the user: %+v (asked %d)", perms[1], asked)
	}
	if len(changes) != 1 || changes[0].CallID != "c1" || !strings.HasSuffix(changes[0].Name, "b.txt") || !strings.HasPrefix(changes[0].Arguments, "absent->") {
		t.Fatalf("file changes = %+v", changes)
	}
	rep, err := session.VerifyAudit(sess.Path())
	if err != nil || !rep.OK() {
		t.Fatalf("audit = %+v, %v", rep, err)
	}
}
