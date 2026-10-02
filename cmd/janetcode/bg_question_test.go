package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// --answer shows a background run's open question and answers it with an
// option number; a question the run already went on from takes a late
// answer, and a permission request pending at the same time is still
// settled by allow/session/deny.
func TestAnswerModeAnswersAQuestion(t *testing.T) {
	dir := t.TempDir()
	path, err := session.BgSessionPath(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	q := &session.BgQuestion{ID: "q1-1", Asked: time.Now(), Proceeded: true, Items: []session.BgQuestionItem{{
		Prompt: "Which port?", Options: []string{"8080", "9000"}, Recommended: "9000",
	}}}
	st := session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: time.Now(), Question: q}
	if err := session.WriteBgStatus(path, st); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := answerMode(&out, dir, "s1", ""); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "Which port?") || !strings.Contains(s, "2. 9000 (recommended)") || !strings.Contains(s, "late answer") {
		t.Fatalf("shown:\n%s", s)
	}
	out.Reset()
	if err := answerMode(&out, dir, "s1", "1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "→ 8080") {
		t.Fatalf("confirmation: %q", out.String())
	}
	if raw, ok := session.TakeBgReply(path, "q1-1"); !ok || len(raw) != 1 || raw[0] != "1" {
		t.Fatalf("reply = %v %v", raw, ok)
	}

	// A permission request pending too: allow still settles it.
	st.State, st.Ask = session.BgWaiting, &session.BgAsk{ID: "a1", Tool: "bash", Summary: "go test", Asked: time.Now()}
	if err := session.WriteBgStatus(path, st); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := answerMode(&out, dir, "s1", "allow"); err != nil {
		t.Fatal(err)
	}
	if d, ok := session.TakeBgAnswer(path, "a1"); !ok || d != "allow" {
		t.Fatalf("permission answer = %q %v", d, ok)
	}
}
