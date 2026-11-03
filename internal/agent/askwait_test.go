package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

const askOneRecommended = `{"questions":[{"prompt":"Which port?","options":[{"label":"8080"},{"label":"9000"}],"recommended":"9000"}]}`

// heldAsker shows the question and answers only when told to, the way a
// person who stepped away does.
type heldAsker struct {
	answer   chan []Answer
	mu       sync.Mutex
	seen     []Question
	timedOut [][]Answer
}

func newHeldAsker() *heldAsker { return &heldAsker{answer: make(chan []Answer, 1)} }

func (h *heldAsker) Ask(ctx context.Context, qs []Question) ([]Answer, error) {
	h.mu.Lock()
	h.seen = qs
	h.mu.Unlock()
	select {
	case a := <-h.answer:
		return a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *heldAsker) AskTimedOut(_ []Question, proceeded []Answer) {
	h.mu.Lock()
	h.timedOut = append(h.timedOut, proceeded)
	h.mu.Unlock()
}

func waitSteer(t *testing.T, ag *Agent) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ag.pendingSteer() > 0 {
			notes, _ := ag.takeSteering()
			return strings.Join(notes, "\n")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no late answer was delivered")
	return ""
}

// Nobody can answer a headless run: with a recommendation the turn goes
// on with it and says so; the choice is labelled the model's own.
func TestAskWithoutAnswererProceedsWithRecommendation(t *testing.T) {
	out, err := (&AskTool{Parent: &Agent{}}).Run(context.Background(), json.RawMessage(askOneRecommended))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "A: 9000 (recommended)") || !strings.Contains(out, "did not choose") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "still open") {
		t.Fatalf("a session with no answerer promised a late answer: %q", out)
	}
}

// The wait runs out: the turn goes on with the recommendation, the asker
// is told, and the question stays open — the answer given afterwards
// reaches the model as a late answer through steering.
func TestAskTimesOutThenDeliversLateAnswer(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(30*time.Millisecond, 30*time.Millisecond)
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(askOneRecommended))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No answer within") || !strings.Contains(out, "A: 9000 (recommended)") || !strings.Contains(out, "still open") {
		t.Fatalf("out = %q", out)
	}
	h.mu.Lock()
	if len(h.timedOut) != 1 || h.timedOut[0][0].Label != "9000" {
		t.Fatalf("timedOut = %+v", h.timedOut)
	}
	if h.seen[0].Deadline.IsZero() || h.seen[0].Recommended != "9000" {
		t.Fatalf("the asker was not given the deadline and recommendation: %+v", h.seen[0])
	}
	h.mu.Unlock()
	h.answer <- []Answer{{Label: "8080"}}
	late := waitSteer(t, ag)
	if !strings.Contains(late, "Late answer") || !strings.Contains(late, "A: 8080") || !strings.Contains(late, "differs") {
		t.Fatalf("late = %q", late)
	}
}

// A late answer that only confirms the assumption says so, rather than
// telling the model to change course.
func TestLateAnswerConfirmingRecommendation(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(20*time.Millisecond, 20*time.Millisecond)
	if _, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(askOneRecommended)); err != nil {
		t.Fatal(err)
	}
	h.answer <- []Answer{{Label: "9000"}}
	if late := waitSteer(t, ag); !strings.Contains(late, "carry on") {
		t.Fatalf("late = %q", late)
	}
}

// Dismissing the open question (esc answers "skipped") is not an answer:
// nothing is sent to the model.
func TestDismissedLateQuestionSendsNothing(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(20*time.Millisecond, 20*time.Millisecond)
	if _, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(askOneRecommended)); err != nil {
		t.Fatal(err)
	}
	h.answer <- []Answer{{Label: "skipped"}}
	time.Sleep(100 * time.Millisecond)
	if n := ag.pendingSteer(); n != 0 {
		t.Fatalf("a dismissed question steered the model (%d notes)", n)
	}
}

// An answer in time is just an answer.
func TestAskAnsweredInTime(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(5*time.Second, 5*time.Second)
	h.answer <- []Answer{{Label: "8080"}}
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(askOneRecommended))
	if err != nil {
		t.Fatal(err)
	}
	if out != "Q: Which port?\nA: 8080" {
		t.Fatalf("out = %q", out)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.timedOut) != 0 {
		t.Fatal("an answered question was reported as timed out")
	}
}

// Without a recommendation there is nothing to go on with: the question
// waits for the user even under a bound, so the harness never invents
// the user's choice.
func TestAskWithoutRecommendationIgnoresTheBound(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(10*time.Millisecond, 10*time.Millisecond)
	done := make(chan string, 1)
	go func() {
		out, _ := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Delete it?","options":[{"label":"Yes"},{"label":"No"}]}]}`))
		done <- out
	}()
	select {
	case out := <-done:
		t.Fatalf("returned without an answer: %q", out)
	case <-time.After(80 * time.Millisecond):
	}
	h.answer <- []Answer{{Label: "No"}}
	if out := <-done; !strings.Contains(out, "A: No") {
		t.Fatalf("out = %q", out)
	}
	// And with nobody to answer at all, it is refused as before.
	if _, err := (&AskTool{Parent: &Agent{}}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Delete it?","options":[{"label":"Yes"},{"label":"No"}]}]}`)); err == nil {
		t.Fatal("answered a question with no recommendation and nobody there")
	}
}

// An interactive session waits as long as it takes by default; a turn the
// harness started on its own (a goal round, a /loop firing) and a --bg
// session are held to the unattended wait.
func TestAskWaitPolicy(t *testing.T) {
	ag := &Agent{}
	if d, alone := ag.AskWaitFor(); d != AskWaitForever || alone {
		t.Fatalf("interactive default = %v, %v", d, alone)
	}
	ag.MarkUnattendedTurn()
	if d, alone := ag.AskWaitFor(); d != DefaultAskWaitUnattended || !alone {
		t.Fatalf("marked turn = %v, %v", d, alone)
	}
	ag.endAskTurn()
	if d, _ := ag.AskWaitFor(); d != AskWaitForever {
		t.Fatalf("the mark outlived its turn: %v", d)
	}
	ag.Unattended = true
	ag.SetAskWait(AskWaitForever, 0)
	if d, alone := ag.AskWaitFor(); d != 0 || !alone {
		t.Fatalf("unattended session = %v, %v", d, alone)
	}
}

// A recommendation that is not one of the options is refused: the turn
// must never go on with a choice the user was not offered.
func TestAskRecommendationMustBeAnOption(t *testing.T) {
	ag := &Agent{}
	_, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Port?","options":[{"label":"8080"},{"label":"9000"}],"recommended":"7000"}]}`))
	if err == nil || !strings.Contains(err.Error(), "not one of its options") {
		t.Fatalf("err = %v", err)
	}
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Port?","options":[{"label":"Fast"},{"label":"Safe"}],"recommended":"safe"}]}`))
	if err != nil || !strings.Contains(out, "A: Safe (recommended)") {
		t.Fatalf("case-insensitive match: %q %v", out, err)
	}
}

// Cancelling the turn before the deadline cancels the question too, as
// it always has.
func TestAskCancelledBeforeDeadline(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = h
	ag.SetAskWait(5*time.Second, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := (&AskTool{Parent: ag}).Run(ctx, json.RawMessage(askOneRecommended)); err == nil {
		t.Fatal("a cancelled turn still answered")
	}
}

type deferHeld struct{ *heldAsker }

func (deferHeld) DeferAsks() bool { return true }

// A background run's question without a recommendation is recorded and the
// tool returns at once; the answer arrives later as a message.
func TestAskDeferredDoesNotBlockTheRun(t *testing.T) {
	ag := &Agent{}
	h := newHeldAsker()
	ag.Asker = deferHeld{h}
	ag.Unattended = true
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Delete it?","options":[{"label":"Yes"},{"label":"No"}]}]}`))
	if err != nil || !strings.Contains(out, "recorded for the user") {
		t.Fatalf("out = %q, %v", out, err)
	}
	h.answer <- []Answer{{Label: "No"}}
	if got := waitSteer(t, ag); !strings.Contains(got, "A: No") || strings.Contains(got, "went on") {
		t.Fatalf("answer message = %q", got)
	}
}

func TestPermModeRank(t *testing.T) {
	if (&Agent{AutoApprove: true}).PermMode() != "yolo" || (&Agent{}).PermMode() != "default" {
		t.Fatal("mode names")
	}
	if !(ModeRank("readonly") < ModeRank("default") && ModeRank("default") < ModeRank("auto-edits") && ModeRank("auto") < ModeRank("yolo")) {
		t.Fatal("rank order")
	}
}
