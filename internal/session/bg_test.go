package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBgStatusRoundTripAndDeadProcess(t *testing.T) {
	p := filepath.Join(t.TempDir(), "abc.jsonl")
	if _, ok, err := ReadBgStatus(p); ok || err != nil {
		t.Fatalf("a session that never ran in the background: ok=%v err=%v", ok, err)
	}
	st := BgStatus{State: BgWaiting, PID: os.Getpid(), Started: time.Now(), Ask: &BgAsk{ID: "a1", Tool: "bash", Summary: "go test"}}
	if err := WriteBgStatus(p, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadBgStatus(p)
	if err != nil || !ok || got.State != BgWaiting || got.Ask == nil || got.Ask.ID != "a1" {
		t.Fatalf("read back %+v ok=%v err=%v", got, ok, err)
	}
	if line := got.Line(time.Now()); line != "waiting for an answer: bash go test" {
		t.Errorf("line = %q", line)
	}
	// A run whose process died without saying how it ended is failed,
	// not waiting forever.
	st.PID = 1 << 30
	WriteBgStatus(p, st)
	got, _, _ = ReadBgStatus(p)
	if got.State != BgFailed || got.Ask != nil || !strings.Contains(got.Detail, "stopped without finishing") {
		t.Errorf("dead run = %+v", got)
	}
}

// Only the answer to the request being waited on is taken; one left over
// from an earlier request is thrown away rather than applied to this one.
func TestTakeBgAnswer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "abc.jsonl")
	if _, ok := TakeBgAnswer(p, "a2"); ok {
		t.Fatal("an answer with none written")
	}
	WriteBgAnswer(p, BgAnswer{ID: "a1", Decision: "allow"})
	if _, ok := TakeBgAnswer(p, "a2"); ok {
		t.Fatal("a stale answer was applied to a later request")
	}
	if _, err := os.Stat(BgAnswerPath(p)); err == nil {
		t.Fatal("the stale answer was left behind")
	}
	WriteBgAnswer(p, BgAnswer{ID: "a2", Decision: "deny"})
	if d, ok := TakeBgAnswer(p, "a2"); !ok || d != "deny" {
		t.Fatalf("answer = %q %v", d, ok)
	}
}
