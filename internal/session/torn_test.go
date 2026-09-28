package session

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// tornSession writes a session, then cuts its file short mid-line the way
// a crash or a power cut between the write and its newline does.
func tornSession(t *testing.T, dir string) *Session {
	t.Helper()
	s, err := New(dir, "/work", "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "first"), llm.TextMessage(llm.RoleAssistant, "answer"))
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Keep every complete line and then half of one more.
	cut := len(data) - 20
	if cut <= 0 || data[cut] == '\n' {
		t.Fatalf("bad fixture: %q", data)
	}
	if err := os.WriteFile(s.Path(), data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// A torn final line must be dropped on Open, and the next Append must
// rewrite the file rather than concatenating onto the wreckage — which
// would leave a corrupt line that is no longer last, and so make every
// later Open refuse the whole session.
func TestTornTailIsRepairedOnNextAppend(t *testing.T) {
	dir := t.TempDir()
	id := tornSession(t, dir).Meta.ID

	reopened, msgs, err := Open(dir, id)
	if err != nil {
		t.Fatalf("open after a torn write: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Text() != "first" {
		t.Fatalf("messages = %v, want the one complete turn", msgs)
	}

	put(t, reopened, llm.TextMessage(llm.RoleUser, "second"))

	raw, err := os.ReadFile(reopened.Path())
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not one record (%v): %q", i+1, err, line)
		}
	}
	again, msgs, err := Open(dir, id)
	if err != nil {
		t.Fatalf("reopen after the repairing append: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Text() != "first" || msgs[1].Text() != "second" {
		t.Fatalf("messages = %v, want first then second", msgs)
	}
	if again.Meta.ID != id {
		t.Fatalf("id = %q, want %q", again.Meta.ID, id)
	}
}

// A metadata write repairs the file the same way an event write does.
func TestTornTailIsRepairedOnMetaUpdate(t *testing.T) {
	dir := t.TempDir()
	id := tornSession(t, dir).Meta.ID

	reopened, _, err := Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetTitle("repaired"); err != nil {
		t.Fatal(err)
	}
	s, msgs, err := Open(dir, id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s.Meta.Title != "repaired" || len(msgs) != 1 {
		t.Fatalf("title = %q, %d messages", s.Meta.Title, len(msgs))
	}
}

// An intact session must not be rewritten for nothing: Append still
// appends, so the durability story (one write per event) is unchanged.
func TestIntactSessionIsNotMarkedTorn(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, llm.TextMessage(llm.RoleUser, "hi"))
	reopened, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.torn {
		t.Fatal("a complete log was flagged torn")
	}
}

// A repair that itself fails — the directory has gone read-only, the
// disk is full — must leave the session still marked torn. It used to
// clear the flag before trying, so the write after the failed one
// appended straight onto the partial line and the next Open refused the
// whole session.
func TestFailedRepairStaysTorn(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write")
	}
	dir := t.TempDir()
	id := tornSession(t, dir).Meta.ID
	reopened, _, err := Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "lost")
	if _, err := reopened.Append(Event{Type: TypeUserMessage, Surface: true, Message: &msg}); err == nil {
		t.Fatal("append into a read-only directory succeeded")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, reopened, llm.TextMessage(llm.RoleUser, "second"))
	if _, msgs, err := Open(dir, id); err != nil {
		t.Fatalf("reopen after a failed repair: %v", err)
	} else if len(msgs) != 2 || msgs[1].Text() != "second" {
		t.Fatalf("messages = %v, want first then second", msgs)
	}
}
