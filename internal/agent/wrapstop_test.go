package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const wrapStopMarker = "Stopped by the harness"

// stopRun plays a turn that writes a small package and goes green, then
// makes the calls of steps ("green", "red", "edit", or "own", an inline
// python check that fails), and a final "Done." that a stopped turn never
// reaches. It returns the agent, how many model calls were made and the
// error Run gave.
func stopRun(t *testing.T, headless bool, off map[string]bool, review bool, steps ...string) (a *Agent, calls int, err error) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	goTest := `{"command":"cd ` + root + ` && go test ./..."}`
	rs := []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.go","content":"package m\n\nfunc A() int { return 1 }\n"}`),
		toolCall("t2", "write_file", `{"path":"a_test.go","content":"package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal() } }\n"}`),
		toolCall("t3", "bash", goTest),
	}
	for i, c := range steps {
		id := fmt.Sprintf("x%d", i)
		switch c {
		case "edit":
			rs = append(rs, toolCall(id, "write_file", fmt.Sprintf(`{"path":"b.go","content":"package m\n\nfunc B%d() int { return 1 }\n"}`, i)))
		case "green":
			rs = append(rs, toolCall(id, "bash", goTest))
		case "red":
			rs = append(rs, toolCall(id, "bash", `{"command":"cd `+root+` && go test ./nonexistent/..."}`))
		case "own":
			rs = append(rs, toolCall(id, "bash", fmt.Sprintf(`{"command":"python3 - <<'EOF'\nassert 1 == 2, %d\nEOF"}`, i)))
		}
	}
	rs = append(rs, text("Done."))
	f := &fake{responses: rs}
	a = &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, WrapUp: headless, WrapStop: headless,
		SpecReview: review, Off: off, Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
	err = a.Run(context.Background(), "add A", nil)
	return a, len(f.requests), err
}

// calls of the turn before the note: three setup calls, then the checks
// that reach wrapUpAfter after the first green.
func stopped(a *Agent) bool {
	last := a.Messages[len(a.Messages)-1]
	return last.Role == llm.RoleAssistant && strings.Contains(last.Text(), wrapStopMarker)
}

// Ten calls after the note, still green: the turn ends there, without the
// model's reply to the tenth, and the run returns no error.
func TestWrapStopEndsTheTurnAfterTenPostNoteCalls(t *testing.T) {
	// 8 green calls bring the note on the eighth, 10 more are counted.
	a, calls, err := stopRun(t, true, nil, false, greens(wrapUpAfter+wrapStopAfter)...)
	if err != nil {
		t.Fatal(err)
	}
	if !stopped(a) {
		t.Fatalf("turn not stopped; last = %q", a.Messages[len(a.Messages)-1].Text())
	}
	if want := 3 + wrapUpAfter + wrapStopAfter; calls != want {
		t.Fatalf("%d model calls, want %d: the closing \"Done.\" must not be asked for", calls, want)
	}
	if !strings.Contains(a.Messages[len(a.Messages)-1].Text(), "go test ./...") {
		t.Fatalf("closing text does not name the check: %q", a.Messages[len(a.Messages)-1].Text())
	}
	// One call fewer: not yet.
	if a, _, _ := stopRun(t, true, nil, false, greens(wrapUpAfter+wrapStopAfter-1)...); stopped(a) {
		t.Fatal("stopped one call early")
	}
}

// The outcome is recorded as for any finished turn: a verdict line and a
// turn/end the session can be resumed from.
func TestWrapStopRecordsTheOutcome(t *testing.T) {
	a, _, err := stopRun(t, true, nil, false, greens(wrapUpAfter+wrapStopAfter)...)
	if err != nil || !stopped(a) {
		t.Fatalf("err = %v, stopped = %v", err, stopped(a))
	}
	var verdict, end *session.Event
	evs := a.Log.Events()
	for i := range evs {
		switch evs[i].Type {
		case session.TypeVerdict:
			verdict = &evs[i]
		case session.TypeTurnEnd:
			end = &evs[i]
		}
	}
	if verdict == nil || verdict.IsError || verdict.Name != string(VerdictVerified) {
		t.Fatalf("verdict event = %+v", verdict)
	}
	if end == nil || end.Reason != "wrapstop" {
		t.Fatalf("turn end = %+v", end)
	}
	if !a.Outcome().OK() {
		t.Fatalf("outcome = %+v", a.Outcome())
	}
}

// Red at the tenth call: no stop; the first green step after it stops.
// The separate failing command remains unresolved, so that forced stop
// reports incomplete verification. An edit after the last run is not green.
func TestWrapStopWaitsForGreen(t *testing.T) {
	steps := append(greens(wrapUpAfter+wrapStopAfter-1), "red")
	if a, _, _ := stopRun(t, true, nil, false, steps...); stopped(a) {
		t.Fatal("stopped on a red state")
	}
	a, calls, err := stopRun(t, true, nil, false, append(steps, "green")...)
	if !errors.Is(err, ErrVerification) || !stopped(a) {
		t.Fatalf("err = %v, stopped = %v, want an incomplete stop at the next green step", err, stopped(a))
	}
	if want := 3 + len(steps) + 1; calls != want {
		t.Fatalf("%d model calls, want %d", calls, want)
	}
	edited := append(greens(wrapUpAfter+wrapStopAfter-1), "edit")
	if a, _, _ := stopRun(t, true, nil, false, edited...); stopped(a) {
		t.Fatal("stopped with an edit after the last run")
	}
}

// The loop this exists for: the tests are green and the model's own check
// is the red one. VerifiedBy is empty then, the project run still counts.
func TestWrapStopCountsProjectTestsOverAFailingOwnCheck(t *testing.T) {
	steps := append(greens(wrapUpAfter), "own", "own", "own", "own", "own", "own", "own", "own", "own", "own")
	a, _, err := stopRun(t, true, nil, false, steps...)
	if !errors.Is(err, ErrVerification) || !stopped(a) {
		t.Fatalf("err = %v, stopped = %v; a failing own check must not report success", err, stopped(a))
	}
}

func TestWrapStopNeedsTheNote(t *testing.T) {
	// The note off: nothing to count from.
	if a, _, _ := stopRun(t, true, map[string]bool{"wrapup": true}, false, greens(wrapUpAfter+wrapStopAfter+3)...); stopped(a) {
		t.Fatal("stopped though the wrap-up note never fired")
	}
}

func TestWrapStopOffAndInteractive(t *testing.T) {
	steps := greens(wrapUpAfter + wrapStopAfter + 2)
	if a, _, _ := stopRun(t, true, map[string]bool{"wrapstop": true}, false, steps...); stopped(a) {
		t.Fatal("stopped with wrapstop off")
	}
	if a, _, _ := stopRun(t, false, nil, false, steps...); stopped(a) {
		t.Fatal("stopped in interactive mode")
	}
}

func TestWrapStopStaysOutOfTheReviewAndDryRun(t *testing.T) {
	a := &Agent{WrapStop: true, Log: &session.Memory{}}
	a.wrap.fired = true
	a.wrapStop.calls = 50
	a.reviewSteps = 1
	if c := a.wrapStopCheck([]llm.Block{{}}, nil, true); c != "" || a.wrapStop.calls != 50 {
		t.Fatalf("stopped or counted during a review: %q, %d", c, a.wrapStop.calls)
	}
}

// A harness stop is not the model saying it is done: the spec review and
// the edge rules, which fire in finishTurn, do not read the change after
// it, and no review note is in the history.
func TestWrapStopSkipsTheSpecReview(t *testing.T) {
	a, _, err := stopRun(t, true, nil, true, greens(wrapUpAfter+wrapStopAfter)...)
	if err != nil || !stopped(a) {
		t.Fatalf("err = %v, stopped = %v", err, stopped(a))
	}
	if a.reviewSteps >= 0 {
		t.Fatalf("a review began after the stop (reviewSteps = %d)", a.reviewSteps)
	}
	for _, m := range a.Messages {
		if strings.Contains(m.Text(), specReviewNote) {
			t.Fatal("the spec review note was sent after the stop")
		}
	}
}

// An edit of a product file after the note restarts the count: the model
// changed the code, so the checks after it are not the loop this ends.
// Without the restart the stop lands on the first green run after it.
func TestWrapStopRestartsAtAProductEdit(t *testing.T) {
	steps := append(greens(wrapUpAfter+wrapStopAfter-2), "edit", "green")
	if a, _, _ := stopRun(t, true, nil, false, steps...); stopped(a) {
		t.Fatal("stopped two calls after a product edit")
	}
	if a, _, _ := stopRun(t, true, map[string]bool{"wrapstopedit": true}, false, steps...); !stopped(a) {
		t.Fatal("wrapstopedit off: want the stop at ten calls after the note")
	}
	// Ten green calls after the edit: stopped again.
	if a, _, _ := stopRun(t, true, nil, false, append(steps, greens(wrapStopAfter-1)...)...); !stopped(a) {
		t.Fatal("not stopped ten calls after the edit")
	}
}

// A model that keeps editing is still ended: the count restarts twice.
func TestWrapStopRestartsAreBounded(t *testing.T) {
	steps := greens(wrapUpAfter)
	for i := 0; i <= wrapStopRestarts; i++ {
		steps = append(steps, "edit")
		steps = append(steps, greens(wrapStopAfter-2)...)
	}
	steps = append(steps, "green")
	a, _, _ := stopRun(t, true, nil, false, steps...)
	if !stopped(a) {
		t.Fatal("a turn that keeps editing was never stopped")
	}
	if a.wrapStop.restarts != wrapStopRestarts {
		t.Fatalf("restarts = %d, want %d", a.wrapStop.restarts, wrapStopRestarts)
	}
}
