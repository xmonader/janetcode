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

// The launcher writes its record before the child exists, with no pid;
// a child that died before saying anything is found by its pid sidecar.
func TestBgStatusWithoutPIDUsesThePIDSidecar(t *testing.T) {
	p := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := WriteBgStatus(p, BgStatus{State: BgRunning, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := ReadBgStatus(p); got.State != BgRunning {
		t.Fatalf("no pid known yet: %+v", got)
	}
	if err := os.WriteFile(strings.TrimSuffix(p, ".jsonl")+".pid", []byte("1073741824\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := ReadBgStatus(p); got.State != BgFailed {
		t.Errorf("a child that died before writing its status = %+v, want failed", got)
	}
}

// One pending question: the same text keeps its time, another replaces it,
// and only an answer from the question it came from clears it.
func TestBgPendingQuestion(t *testing.T) {
	var st BgStatus
	t0 := time.Now().Add(-time.Hour)
	st.SetPending(BgPending{Text: "Which db?", Ref: "q1", Asked: t0})
	st.SetPending(BgPending{Text: "Which db?", Ref: "q1", Asked: time.Now()})
	if !st.Pending.Asked.Equal(t0) {
		t.Fatalf("asking again moved the time: %v", st.Pending.Asked)
	}
	st.SetPending(BgPending{Text: "Which port?", Ref: "q2"})
	st.ClearPending("q1")
	if st.Pending == nil || st.Pending.Text != "Which port?" {
		t.Fatalf("an old answer cleared the new question: %+v", st.Pending)
	}
	st.ClearPending("q2")
	if st.Pending != nil {
		t.Fatal("not cleared")
	}
	st.SetPending(BgPending{Text: "x"})
	st.State, st.Ended = BgDone, time.Now()
	if !strings.Contains(st.Line(time.Now()), "finished with a question waiting") {
		t.Fatalf("line = %q", st.Line(time.Now()))
	}
}

// An approval request takes the pending slot from an open question and
// gives it back when it clears.
func TestClearPendingRestoresOpenQuestion(t *testing.T) {
	st := &BgStatus{Question: &BgQuestion{ID: "q1", Asked: time.Now(), Items: []BgQuestionItem{{Prompt: "Port?", Options: []string{"1", "2"}}}}}
	st.SetPending(BgPending{Text: "Port?", Ref: "q1"})
	st.SetPending(BgPending{Text: "May it run bash?", Ref: "a1"})
	st.ClearPending("a1")
	if st.Pending == nil || st.Pending.Ref != "q1" || st.Pending.Text != "Port?" || len(st.Pending.Choices) != 2 {
		t.Fatalf("pending = %+v", st.Pending)
	}
	st.Question = nil
	st.ClearPending("q1")
	if st.Pending != nil {
		t.Fatalf("pending = %+v", st.Pending)
	}
}

func TestAckBgPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	p := &BgPending{Text: "x?", Ref: "q1"}
	_ = WriteBgStatus(path, BgStatus{State: BgRunning, Pending: p})
	if AckBgPending(path) {
		t.Fatal("a running run's question was cleared")
	}
	_ = WriteBgStatus(path, BgStatus{State: BgDone, Pending: p})
	if !AckBgPending(path) {
		t.Fatal("not cleared")
	}
	if st, _, _ := ReadBgStatus(path); st.Pending != nil {
		t.Fatalf("pending = %+v", st.Pending)
	}
}
