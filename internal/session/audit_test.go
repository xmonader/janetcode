package session

import (
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func auditedSession(t *testing.T) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	m := llm.TextMessage(llm.RoleUser, "delete the temp files")
	for _, e := range []Event{
		{Type: TypeUserMessage, Surface: true, Turn: 1, Message: &m},
		{Type: TypePermission, Turn: 1, CallID: "c1", Name: "bash", Reason: "the user: allow"},
		{Type: TypeToolCall, Turn: 1, CallID: "c1", Name: "bash", Arguments: `{"command":"rm -f /tmp/x"}`},
		{Type: TypeToolResult, Turn: 1, CallID: "c1", Name: "bash", Output: "(no output)"},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return s, dir
}

func verify(t *testing.T, path string) AuditReport {
	t.Helper()
	rep, err := VerifyAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func rewriteFile(t *testing.T, path string, fn func(string) string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fn(string(b))), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuditChainVouchesForEveryEvent(t *testing.T) {
	s, _ := auditedSession(t)
	rep := verify(t, s.Path())
	if !rep.OK() || rep.Events != 4 || rep.Records != 5 || rep.Before != 0 || rep.Head == "" {
		t.Fatalf("fresh session: %+v", rep)
	}
	// A metadata rewrite rewrites every event line; the digests hold.
	if err := s.SetTitle("cleanup"); err != nil {
		t.Fatal(err)
	}
	if rep := verify(t, s.Path()); !rep.OK() || rep.Events != 4 {
		t.Fatalf("after a rewrite: %+v", rep)
	}
}

func TestAuditCatchesAChangedEvent(t *testing.T) {
	s, _ := auditedSession(t)
	rewriteFile(t, s.Path(), func(b string) string { return strings.Replace(b, "rm -f /tmp/x", "echo harmless", 1) })
	rep := verify(t, s.Path())
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, "\n"), "was changed after it was written") {
		t.Fatalf("an edited tool call went unnoticed: %+v", rep)
	}
}

func TestAuditCatchesARemovedEvent(t *testing.T) {
	s, _ := auditedSession(t)
	rewriteFile(t, s.Path(), func(b string) string {
		var keep []string
		for _, l := range strings.Split(b, "\n") {
			if !strings.Contains(l, `"t":"permission"`) {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	})
	rep := verify(t, s.Path())
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, "\n"), "was removed") {
		t.Fatalf("a removed permission record went unnoticed: %+v", rep)
	}
}

func TestAuditCatchesAnEditedRecord(t *testing.T) {
	s, _ := auditedSession(t)
	rewriteFile(t, AuditPath(s.Path()), func(b string) string { return strings.Replace(b, `"t":"permission"`, `"t":"notice"`, 1) })
	rep := verify(t, s.Path())
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, "\n"), "record 3 was edited") {
		t.Fatalf("an edited audit record went unnoticed: %+v", rep)
	}
}

// An event appended by something that bypassed the session (another
// writer, a hand edit) has no record.
func TestAuditCatchesAnInsertedEvent(t *testing.T) {
	s, _ := auditedSession(t)
	rewriteFile(t, s.Path(), func(b string) string {
		return b + `{"type":"event","seq":99,"at":"2026-01-01T00:00:00Z","t":"notice","reason":"planted"}` + "\n"
	})
	rep := verify(t, s.Path())
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, "\n"), "seq 99") {
		t.Fatalf("an inserted event went unnoticed: %+v", rep)
	}
}

// A session recorded before the audit log existed gets one from its next
// event on, and the report says how many came before.
func TestAuditStartsOnAnOldSession(t *testing.T) {
	s, dir := auditedSession(t)
	if err := os.Remove(AuditPath(s.Path())); err != nil {
		t.Fatal(err)
	}
	re, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := re.Append(Event{Type: TypeNotice, Reason: "resumed"}); err != nil {
		t.Fatal(err)
	}
	rep := verify(t, s.Path())
	if !rep.OK() || rep.Before != 4 || rep.Events != 1 || rep.From != 5 {
		t.Fatalf("old session resumed: %+v", rep)
	}
}

// A fork's copied events are vouched for by the fork's own log, and a
// resumed session's chain carries on from where it was.
func TestAuditForkAndResume(t *testing.T) {
	s, dir := auditedSession(t)
	f, err := Fork(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append(Event{Type: TypeNotice, Reason: "in the fork"}); err != nil {
		t.Fatal(err)
	}
	if rep := verify(t, f.Path()); !rep.OK() || rep.Events != 5 {
		t.Fatalf("fork: %+v", rep)
	}
	re, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := re.Append(Event{Type: TypeNotice, Reason: "resumed"}); err != nil {
		t.Fatal(err)
	}
	if rep := verify(t, s.Path()); !rep.OK() || rep.Events != 5 || rep.Records != 6 {
		t.Fatalf("resumed: %+v", rep)
	}
}
