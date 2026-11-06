package headless

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

func bgAsker(t *testing.T) (*BgAsker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	st := &session.BgStatus{State: session.BgRunning, Started: time.Now()}
	inbox := &InboxApprover{SessionPath: path, SessionID: "s", Status: st, Log: io.Discard}
	return &BgAsker{Inbox: inbox, Poll: 5 * time.Millisecond}, path
}

func portQuestion() []agent.Question {
	return []agent.Question{{Prompt: "Which port?", Options: []agent.AskOption{{Label: "8080"}, {Label: "9000"}}, Recommended: "9000"}}
}

// openQuestion waits for the run to record its question in the sidecar.
func openQuestion(t *testing.T, path string) *session.BgQuestion {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok, _ := session.ReadBgStatus(path); ok && st.Question != nil {
			return st.Question
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the question was never recorded")
	return nil
}

// The question waits in the status sidecar, the run says it is waiting,
// and a reply from --answer (a number here) comes back as the option.
func TestBgAskerWaitsForAnAnswer(t *testing.T) {
	b, path := bgAsker(t)
	got := make(chan []agent.Answer, 1)
	go func() {
		ans, _ := b.Ask(context.Background(), portQuestion())
		got <- ans
	}()
	q := openQuestion(t, path)
	if st, _, _ := session.ReadBgStatus(path); st.State != session.BgWaiting || !strings.Contains(st.Line(time.Now()), "Which port?") {
		t.Fatalf("status = %+v", st)
	}
	if q.Items[0].Recommended != "9000" || len(q.Items[0].Options) != 2 {
		t.Fatalf("question = %+v", q)
	}
	if err := session.WriteBgReply(path, session.BgReply{ID: q.ID, Answers: []string{"1"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case ans := <-got:
		if len(ans) != 1 || ans[0].Label != "8080" {
			t.Fatalf("answers = %+v", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer never arrived")
	}
	if st, _, _ := session.ReadBgStatus(path); st.Question != nil || st.State != session.BgRunning {
		t.Fatalf("the answered question is still open: %+v", st)
	}
}

// Past the wait the run is running again and the question stays in the
// sidecar, marked proceeded, for a late answer.
func TestBgAskerTimedOutKeepsTheQuestionOpen(t *testing.T) {
	b, path := bgAsker(t)
	go b.Ask(context.Background(), portQuestion())
	openQuestion(t, path)
	b.AskTimedOut(portQuestion(), nil)
	st, _, _ := session.ReadBgStatus(path)
	if st.State != session.BgRunning || st.Question == nil || !st.Question.Proceeded {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(st.Line(time.Now()), "late answer") {
		t.Fatalf("the listing does not say a late answer is taken: %q", st.Line(time.Now()))
	}
}

// End to end through the ask tool: a --bg run goes on with the
// recommendation after the unattended wait, and the answer --answer
// writes afterwards reaches the model as a late answer.
func TestBgRunGoesOnThenTakesLateAnswer(t *testing.T) {
	b, path := bgAsker(t)
	ag := &agent.Agent{}
	ag.Asker, ag.Unattended = b, true
	ag.SetAskWait(agent.AskWaitForever, 50*time.Millisecond)
	out, err := (&agent.AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Which port?","options":[{"label":"8080"},{"label":"9000"}],"recommended":"9000"}]}`))
	if err != nil || !strings.Contains(out, "A: 9000 (recommended)") {
		t.Fatalf("out = %q, %v", out, err)
	}
	q := openQuestion(t, path)
	if !q.Proceeded {
		t.Fatalf("the question was not marked proceeded: %+v", q)
	}
	if err := session.WriteBgReply(path, session.BgReply{ID: q.ID, Answers: []string{"8080"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, _ := session.ReadBgStatus(path); st.Question == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the late answer was never taken")
}

func TestParseBgAnswers(t *testing.T) {
	qs := append(portQuestion(), agent.Question{Prompt: "Name?", Options: []agent.AskOption{{Label: "alpha"}, {Label: "beta"}}})
	got := ParseBgAnswers(qs, []string{"9000", "something else"})
	if got[0].Label != "9000" || got[1].Label != agent.OtherLabel || got[1].Reason != "something else" {
		t.Fatalf("got %+v", got)
	}
	got = ParseBgAnswers(qs, []string{"2"})
	if got[0].Label != "9000" || got[1].Label != "skipped" {
		t.Fatalf("got %+v", got)
	}
}

// A question with no recommendation does not put the run in the waiting
// state, sets the pending question, asking it again reuses it, and only
// the answer clears it.
func TestBgAskerPendingQuestion(t *testing.T) {
	b, path := bgAsker(t)
	qs := []agent.Question{{Prompt: "Delete it?", Options: []agent.AskOption{{Label: "Yes"}, {Label: "No"}}}}
	go func() { _, _ = b.Ask(context.Background(), qs) }()
	q := openQuestion(t, path)
	st, _, _ := session.ReadBgStatus(path)
	if st.State != session.BgRunning || st.Pending == nil || st.Pending.Text != "Delete it?" || len(st.Pending.Choices) != 2 {
		t.Fatalf("status = %+v pending %+v", st, st.Pending)
	}
	asked := st.Pending.Asked
	// The repeat returns at once (no waiter of its own) and delivers nothing.
	again, err := b.Ask(context.Background(), qs)
	if err != nil || len(again) != 1 || again[0].Label != "skipped" || b.waiters.Load() != 1 {
		t.Fatalf("repeat = %v %v, waiters %d", again, err, b.waiters.Load())
	}
	st, _, _ = session.ReadBgStatus(path)
	if st.Question.ID != q.ID || !st.Pending.Asked.Equal(asked) {
		t.Fatalf("the same question was not reused: %+v", st.Question)
	}
	if err := session.WriteBgReply(path, session.BgReply{ID: q.ID, Answers: []string{"2"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, _ = session.ReadBgStatus(path); st.Pending == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the answer did not clear the pending question")
}

// A mixed batch (one question with a recommendation, one without) is not
// recommended() for the ask tool, so the run goes on and is not waiting.
func TestBgAskerMixedRecommendationsDoNotHold(t *testing.T) {
	b, path := bgAsker(t)
	opts := []agent.AskOption{{Label: "a"}, {Label: "b"}}
	qs := []agent.Question{{Prompt: "one?", Options: opts, Recommended: "a"}, {Prompt: "two?", Options: opts}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// The waiter writes the status file as it unwinds; it must be gone
	// before TempDir's cleanup removes the directory under it.
	defer func() { cancel(); <-done }()
	go func() { defer close(done); _, _ = b.Ask(ctx, qs) }()
	openQuestion(t, path)
	if st, _, _ := session.ReadBgStatus(path); st.State != session.BgRunning {
		t.Fatalf("state = %q, want running", st.State)
	}
}

// A waiter whose question was replaced by another stops polling.
func TestBgAskerReplacedQuestionStopsWaiting(t *testing.T) {
	b, path := bgAsker(t)
	qs := []agent.Question{{Prompt: "first?", Options: []agent.AskOption{{Label: "a"}, {Label: "b"}}}}
	done := make(chan error, 1)
	go func() { _, err := b.Ask(context.Background(), qs); done <- err }()
	q := openQuestion(t, path)
	if err := b.Inbox.Update(func(st *session.BgStatus) { st.Question = &session.BgQuestion{ID: "other"} }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != errBgQuestionReplaced {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the waiter for %s kept polling", q.ID)
	}
}
