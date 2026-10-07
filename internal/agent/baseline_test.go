package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/verify"
)

// The check prints whatever failures.txt lists and fails while it lists
// any: the test controls what is "already failing" through the file.
const baselineCheck = "cat failures.txt; test ! -s failures.txt"

func baselineAgent(t *testing.T, f *fake, failing string) (*Agent, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX check command")
	}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.VerifyBaseline = true
	if err := os.WriteFile(filepath.Join(root, "failures.txt"), []byte(failing), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{baselineCheck}}}
	return a, root
}

func notices(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == EvNotice {
			b.WriteString(e.Text + "\n")
		}
	}
	return b.String()
}

// A failure that was there before the turn is not repaired in a loop: the
// turn changed an unrelated file and the check fails exactly as it did,
// so the model is reminded once (the request may be about it) and the
// turn ends when it answers, without spending a repair round.
func TestBaselineDoesNotRepairWhatWasAlreadyBroken(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"other.txt","content":"x"}`),
		text("done"),
		text("done; TestOld was failing before"),
		text("must not be asked for"),
	}}
	a, _ := baselineAgent(t, f, "--- FAIL: TestOld\n")
	evs, emit := collect()
	if err := a.Run(context.Background(), "change other.txt", emit); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("want exactly one reminder: %d requests\n%s", len(f.requests), notices(*evs))
	}
	msgs := f.requests[2].Messages
	if r := msgs[len(msgs)-1].Text(); !strings.Contains(r, "already failing before this turn") || !strings.Contains(r, "TestOld") {
		t.Fatalf("reminder = %q", r)
	}
	n := notices(*evs)
	if !strings.Contains(n, "reminding the model once") || strings.Contains(n, "sending the model back to fix it") {
		t.Fatalf("notices:\n%s", n)
	}
}

// A failure the turn added goes back, naming what is new and what was
// already broken.
func TestBaselineSendsBackWhatTheTurnBroke(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"failures.txt","content":"--- FAIL: TestOld\n--- FAIL: TestNew\n"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"failures.txt","content":"--- FAIL: TestOld\n"}`),
		text("fixed"),
		text("TestOld was failing before; done"),
	}}
	a, _ := baselineAgent(t, f, "--- FAIL: TestOld\n")
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	// One repair round for TestNew, then one reminder about TestOld.
	if len(f.requests) != 5 {
		t.Fatalf("requests = %d, want 5 (a repair round, then one reminder)", len(f.requests))
	}
	msgs := f.requests[2].Messages
	fb := msgs[len(msgs)-1].Text()
	if !strings.Contains(fb, "TestNew") || !strings.Contains(fb, "Already failing before this turn") || !strings.Contains(fb, "TestOld") {
		t.Fatalf("feedback = %q", fb)
	}
}

// Off, the gate is today's: the pre-existing failure goes back.
func TestBaselineOffKeepsTheOldGate(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"other.txt","content":"x"}`),
		text("done"),
		text("still done"),
		text("still done"),
		text("still done"),
	}}
	a, _ := baselineAgent(t, f, "--- FAIL: TestOld\n")
	a.VerifyBaseline = false
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) < 3 {
		t.Fatalf("without a baseline the failure should go back: %d requests", len(f.requests))
	}
}

// A turn that changes nothing never waits on the baseline, and the run is
// stopped when the turn ends.
func TestBaselineDoesNotHoldAReadOnlyTurn(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("just an answer")}}
	a, _ := baselineAgent(t, f, "")
	a.Verify = &verify.Runner{Cwd: a.Root, Config: verify.Config{Commands: []string{"sleep 30"}}}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), "what is this?", nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a read-only turn waited for the baseline")
	}
	if a.baseline != nil {
		t.Fatal("the baseline outlived the turn")
	}
}
