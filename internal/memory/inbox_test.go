package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestProcessFlush(t *testing.T) {
	for _, c := range []struct {
		name, in string
		res      FlushResult
	}{
		{"empty", "  ", FlushNothing},
		{"no reply", "NO_REPLY", FlushNothing},
		{"no reply fenced", "`NO_REPLY`.", FlushNothing},
		{"prose", "Sure, here is what we did today.", FlushRejected},
		{"note", "## Decisions\n- chose sqlite because x", FlushAccepted},
		{"huge", "## A\n" + strings.Repeat("x", 3*MaxObservation), FlushAccepted},
	} {
		got, res := ProcessFlush(c.in)
		if res != c.res {
			t.Errorf("%s: res = %v", c.name, res)
		}
		if len(got) > MaxObservation+100 {
			t.Errorf("%s: %d bytes", c.name, len(got))
		}
	}
}

func TestFlushDeltaUsesPreviousNote(t *testing.T) {
	f := &fakeProvider{reply: "## Problems\n- fixed the lexer"}
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "fix it"), llm.TextMessage(llm.RoleAssistant, "done")}
	note, res, err := Flush(context.Background(), f, "m", msgs, "")
	if err != nil || res != FlushAccepted || note == "" {
		t.Fatalf("%q %v %v", note, res, err)
	}
	if strings.Contains(f.req.System, "Previous flush") {
		t.Error("first flush must not be a delta")
	}
	if _, _, err := Flush(context.Background(), f, "m", msgs, "## Old\n- thing"); err != nil || !strings.Contains(f.req.System, "- thing") {
		t.Errorf("delta prompt missing previous note: %v", err)
	}
}

func TestInboxLifecycle(t *testing.T) {
	st := Open(t.TempDir(), t.TempDir())
	user, _, err := st.Put(Entry{Name: "mine", Description: "my own rule about commits", Type: TypeFeedback, Scope: "project", Body: "small commits", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	a, added, err := st.AddObservation("s1", "## Decisions\n- picked sqlite for the cache because it needs no daemon")
	if err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	if _, added, _ := st.AddObservation("s1", "## Decisions\n- picked sqlite for the cache because it needs no daemon"); added {
		t.Error("repeat should not be filed twice")
	}
	if _, _, err := st.AddObservation("s1", "## Env\nAWS key AKIAIOSFODNN7EXAMPLE and token"); err == nil {
		t.Error("credential must be refused")
	}
	if last, ok := st.LastObservation("s1"); !ok || last.ID != a.ID || !strings.Contains(last.Body, "sqlite") {
		t.Fatalf("last = %+v %v", last, ok)
	}
	if _, ok := st.LastObservation("other"); ok {
		t.Error("other session has no note")
	}

	// /dream reads the inbox and turns it into an entry; user entries stay.
	f := &fakeProvider{reply: `{"action":"merge","name":"cache-sqlite","type":"decision","description":"cache uses sqlite","body":"No daemon needed.","replaces":[]}`}
	ops, err := DreamWith(context.Background(), f, "m", st.Entries(), st.Observations())
	if err != nil || len(ops) != 1 {
		t.Fatalf("dream: %v %v", ops, err)
	}
	if !strings.Contains(f.req.System, "INBOX") || !strings.Contains(f.req.Messages[0].Text(), "sqlite for the cache") {
		t.Error("inbox not offered to the model")
	}
	if n, err := st.ApplyDream(ops); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	st.MarkConsumed(st.Observations())
	if len(st.Observations()) != 0 {
		t.Error("consumed notes still listed")
	}
	if got, ok := st.Get(user.Name); !ok || got.Body != "small commits" {
		t.Error("user entry touched")
	}
	if _, ok := st.Get("cache-sqlite"); !ok {
		t.Error("merged entry missing")
	}
}

func TestInboxCap(t *testing.T) {
	st := Open(t.TempDir(), t.TempDir())
	for i := 0; i < MaxInbox+3; i++ {
		body := "## N\n" + strings.Repeat("alpha"+string(rune('a'+i%26))+string(rune('a'+i/26))+" ", 10) + strings.Repeat(string(rune('a'+i))+"zq ", 5)
		if _, _, err := st.AddObservation("s", body); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(st.Observations()); n > MaxInbox {
		t.Fatalf("inbox = %d", n)
	}
}
