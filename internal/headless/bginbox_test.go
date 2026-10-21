package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

func waitAsk(t *testing.T, path string) *session.BgAsk {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if st, ok, _ := session.ReadBgStatus(path); ok && st.State == session.BgWaiting && st.Ask != nil {
			return st.Ask
		}
	}
	t.Fatal("the request never showed up in the status file")
	return nil
}

// A background run's permission request waits in the status file until
// --answer settles it; the answer decides, the status goes back to
// running, and the notify command hears about the request.
func TestInboxApproverWaitsForTheAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the notify command below is POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	heard := filepath.Join(dir, "heard")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	st := &session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: time.Now()}
	var log bytes.Buffer
	a := &InboxApprover{SessionPath: path, SessionID: "s1", Cwd: dir, Status: st, Log: &log, Poll: 10 * time.Millisecond,
		Notify: `printf '%s|%s|%s' "$JANETCODE_ASK" "$JANETCODE_ANSWER" "$OPENAI_API_KEY" > ` + heard}

	type result struct {
		d   agent.Decision
		err error
	}
	for _, c := range []struct {
		answer string
		want   agent.Decision
	}{{"allow", agent.Allow}, {"session", agent.AllowSession}, {"deny", agent.Deny}} {
		done := make(chan result, 1)
		go func() {
			d, _, err := a.ApproveWithReason(context.Background(), agent.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)})
			done <- result{d, err}
		}()
		ask := waitAsk(t, path)
		if ask.Tool != "bash" || !strings.Contains(ask.Summary, "go test") {
			t.Fatalf("ask = %+v", ask)
		}
		session.WriteBgAnswer(path, session.BgAnswer{ID: ask.ID, Decision: c.answer})
		select {
		case r := <-done:
			if r.err != nil || r.d != c.want {
				t.Fatalf("%s: decision %v err %v", c.answer, r.d, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the answer was never taken", c.answer)
		}
		if got, _, _ := session.ReadBgStatus(path); got.State != session.BgRunning || got.Ask != nil {
			t.Fatalf("status after the answer = %+v", got)
		}
	}
	b, _ := os.ReadFile(heard)
	if got := string(b); !strings.HasPrefix(got, "bash ") || !strings.Contains(got, "janetcode --answer s1 allow") || strings.Contains(got, "sk-secret") {
		t.Errorf("notify heard %q", got)
	}
}

// Cancelling the run while it waits denies and puts the status back.
func TestInboxApproverCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	st := &session.BgStatus{State: session.BgRunning, Started: time.Now()}
	a := &InboxApprover{SessionPath: path, SessionID: "s1", Status: st, Log: &bytes.Buffer{}, Poll: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan agent.Decision, 1)
	go func() {
		d, _, _ := a.ApproveWithReason(ctx, agent.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
		done <- d
	}()
	waitAsk(t, path)
	cancel()
	if d := <-done; d != agent.Deny {
		t.Fatalf("decision = %v", d)
	}
	if got, _, _ := session.ReadBgStatus(path); got.State != session.BgRunning {
		t.Fatalf("status = %+v", got)
	}
}

// The notify command is bounded: when its time is up it ends with
// everything it started. Only the shell used to be killed, and a child
// still holding the output pipe kept the run waiting, answer or no
// answer, for as long as that child ran.
func TestInboxNotifyEndsWithItsChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the notify command below is POSIX shell")
	}
	var log bytes.Buffer
	a := &InboxApprover{Cwd: t.TempDir(), Notify: "sleep 30; true", Log: &log}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		a.notify(ctx, "bash ls", "janetcode --answer s1 allow")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("notify outlived its bound: the notifier's child kept it waiting")
	}
}
