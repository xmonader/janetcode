package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A turn that changed files and passed its checks is sent back once to
// hold the change against the task; the next "done" ends the turn.
func TestSpecReviewSendsAPassingTurnBackOnce(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	run := func(off map[string]bool) *fake {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"a.go","content":"package m\n\nfunc A() int { return 1 }\n"}`),
			toolCall("t2", "write_file", `{"path":"a_test.go","content":"package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal() } }\n"}`),
			toolCall("t3", "bash", `{"command":"cd `+root+` && go test ./..."}`),
			text("Done; the tests pass."),
			text("Every requirement holds."),
		}}
		a := &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, SpecReview: true, Off: off,
			Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
		if err := a.Run(context.Background(), "add A", nil); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := run(nil)
	if len(f.requests) != 5 {
		t.Fatalf("%d model calls, want 5: the passing turn goes back once", len(f.requests))
	}
	if got := diagLastText(f.requests[4]); !strings.Contains(got, "requirement by requirement") {
		t.Fatalf("last request ends with %q", got)
	}
	if f := run(map[string]bool{"specreview": true}); len(f.requests) != 4 {
		t.Fatalf("%d model calls with specreview off, want 4", len(f.requests))
	}
}

// No changes, or checks that did not pass, are not reviewed.
func TestSpecReviewSkipsUncheckedTurns(t *testing.T) {
	a := &Agent{SpecReview: true}
	for _, v := range []Verdict{VerdictNoChanges, VerdictUnverified, VerdictCheckFailed} {
		var reviewed bool
		if a.specReview(TurnOutcome{Verdict: v}, &reviewed, func(Event) {}) {
			t.Fatalf("%s was sent back for review", v)
		}
	}
}

// The review's sixth tool step carries the note to wrap up.
func TestSpecReviewWrapsUpAfterItsSteps(t *testing.T) {
	a := &Agent{SpecReview: true}
	a.reviewSteps = -1
	if a.reviewStepNote() != "" || a.reviewSteps != -1 {
		t.Fatal("counted a step before the review")
	}
	var reviewed bool
	if !a.specReview(TurnOutcome{Verdict: VerdictVerified}, &reviewed, func(Event) {}) {
		t.Fatal("a verified turn was not reviewed")
	}
	for i := 1; i <= maxReviewSteps+2; i++ {
		got := a.reviewStepNote()
		if (got != "") != (i == maxReviewSteps) {
			t.Fatalf("step %d note = %q", i, got)
		}
	}
}
