package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/session"
)

func TestBackgroundChildArgs(t *testing.T) {
	got := backgroundChildArgs("sess-1", "write hi", []string{"/tmp/a", "/tmp/b"}, nil)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--resume sess-1") || !strings.Contains(joined, "-p write hi") || strings.Contains(joined, "--yolo") {
		t.Fatalf("args = %v", got)
	}
	if strings.Contains(joined, "--bg") {
		t.Fatal("child must not see --bg")
	}
	if !strings.Contains(joined, "--add-dir /tmp/a") || !strings.Contains(joined, "--add-dir /tmp/b") {
		t.Fatalf("extra dirs missing: %v", got)
	}
}

// The background child runs with its stdin closed, so `git diff |
// janetcode --bg -p review` only works if the parent folds the pipe into
// the prompt it hands over. Too much for a command line is an error, not
// a silent half-review.
func TestBgPromptCarriesPipedStdin(t *testing.T) {
	got, err := bgPrompt("review this", "")
	if err != nil || got != "review this" {
		t.Fatalf("no stdin: %q, %v", got, err)
	}

	got, err = bgPrompt("review this", "diff --git a/x b/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "review this") || !strings.Contains(got, "<stdin>\ndiff --git a/x b/x\n</stdin>") {
		t.Fatalf("stdin not folded in: %q", got)
	}

	if _, err := bgPrompt("review", strings.Repeat("x", maxBackgroundStdin+1)); err == nil {
		t.Fatal("oversized stdin was accepted")
	} else if !errors.Is(err, headless.ErrBadInput) {
		t.Fatalf("error is not a usage error: %v", err)
	}
}

// The permission flags given with --bg reach the child, and with none it
// asks (through --answer) rather than running --yolo.
func TestBgPermissionArgs(t *testing.T) {
	cases := []struct {
		o    options
		want string
	}{
		{options{}, ""},
		{options{yolo: true}, "--yolo"},
		{options{readonly: true}, "--readonly"},
		{options{autoEdits: true, deny: "bash(rm *)"}, "--auto-edits --deny bash(rm *)"},
		{options{autoMode: true, sandboxOn: true}, "--auto --sandbox"},
		{options{allow: "bash(go test *)"}, "--allow bash(go test *)"},
	}
	for _, c := range cases {
		if got := strings.Join(bgPermissionArgs(&c.o), " "); got != c.want {
			t.Errorf("%+v: %q, want %q", c.o, got, c.want)
		}
	}
	args := strings.Join(backgroundChildArgs("s", "p", nil, []string{"--readonly"}), " ")
	if strings.Contains(args, "--yolo") || !strings.Contains(args, "--readonly") {
		t.Errorf("child args = %q", args)
	}
	if args := strings.Join(backgroundChildArgs("s", "p", nil, nil), " "); strings.Contains(args, "--yolo") {
		t.Errorf("--bg with no permission flag runs %q", args)
	}
}

// The flags that shape the run reach the child too, which reopens the
// session with --resume and would otherwise run on the configured model
// with no spending cap. Each one parses back to the value it came from.
func TestBgRunArgs(t *testing.T) {
	if got := bgRunArgs(&options{}); len(got) != 0 {
		t.Fatalf("no run flags given, child gets %v", got)
	}
	o := &options{provider: "openrouter", model: "m-1", effort: "high", maxCost: 2.5, maxTurns: 7, compactAt: 90000,
		until: "go test ./...", appendSystem: "-be brief", sets: stringList{"escalate_after=1", "decisions.triage=true"}}
	fs, got := newFlagSet()
	if err := fs.Parse(append(bgRunArgs(o), "-p", "x")); err != nil {
		t.Fatal(err)
	}
	if got.provider != o.provider || got.model != o.model || got.effort != o.effort || got.maxCost != o.maxCost ||
		got.maxTurns != o.maxTurns || got.compactAt != o.compactAt || got.until != o.until || got.appendSystem != o.appendSystem ||
		strings.Join(got.sets, "|") != strings.Join(o.sets, "|") {
		t.Fatalf("child parsed %+v from %v", got, bgRunArgs(o))
	}
}

// A settled session keeps its background status in --sessions: the
// "(settled)" mark used to replace the aside instead of joining it.
func TestSessionListingKeepsBgStatusOfSettledSession(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Chdir(work)
	dir, _ := session.Dir()
	sess, err := session.New(dir, work, "openai", "m")
	if err != nil {
		t.Fatal(err)
	}
	sess.EnsureWritten()
	if err := sess.SetSettled(true); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteBgStatus(sess.Path(), session.BgStatus{State: session.BgDone, Started: time.Now(), Ended: time.Now(), Detail: "all done"}); err != nil {
		t.Fatal(err)
	}
	stdout, _ := captureStd(t)
	if err := printSessions(); err != nil {
		t.Fatal(err)
	}
	if got := stdout(); !strings.Contains(got, "[background: done") || !strings.Contains(got, "(settled)") {
		t.Errorf("--sessions lost the settled mark or the background status:\n%s", got)
	}
}

// A --bg run in a mode that still asks waits for --answer: the request is
// in --sessions and --answer shows it, allow lets the command run, and the
// run is recorded as done with its answer.
func TestBgRunWaitsForAnswer(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(work)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		if calls == 1 {
			io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo ran > ran.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"Ran it."},"finish_reason":"stop"}]}`+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("JANETCODE_PROVIDER", "openai")
	t.Setenv("JANETCODE_MODEL", "m")
	t.Setenv(headless.BgEnv, "1")

	dir, _ := session.Dir()
	sess, err := session.New(dir, work, "openai", "m")
	if err != nil {
		t.Fatal(err)
	}
	sess.EnsureWritten()
	id := sess.Meta.ID

	stdout, _ := captureStd(t)
	done := make(chan error, 1)
	go func() { done <- run([]string{"--resume", id, "-p", "make ran.txt", "--auto-edits"}) }()

	var st session.BgStatus
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, _, _ = session.ReadBgStatus(sess.Path()); st.State == session.BgWaiting {
			break
		}
	}
	if st.State != session.BgWaiting || st.Ask == nil || st.Ask.Tool != "bash" {
		t.Fatalf("the run is not waiting for an answer: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(work, "ran.txt")); err == nil {
		t.Fatal("the command ran before it was answered")
	}
	var out bytes.Buffer
	if err := answerMode(&out, dir, id, ""); err != nil || !strings.Contains(out.String(), "echo ran") {
		t.Fatalf("--answer without a decision: %v\n%s", err, out.String())
	}
	if err := answerMode(&out, dir, id, "maybe"); headless.ExitCode(err) != headless.ExitBadInput {
		t.Fatalf("a bad decision: %v", err)
	}
	if err := answerMode(&out, dir, id, "allow"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not finish after the answer")
	}
	stdout()
	if b, _ := os.ReadFile(filepath.Join(work, "ran.txt")); strings.TrimSpace(string(b)) != "ran" {
		t.Fatalf("the allowed command did not run: %q", b)
	}
	st, _, _ = session.ReadBgStatus(sess.Path())
	if st.State != session.BgDone || st.Detail != "Ran it." {
		t.Fatalf("final status = %+v", st)
	}
	if err := answerMode(&out, dir, id, "allow"); headless.ExitCode(err) != headless.ExitBadInput || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("answering a finished run: %v", err)
	}
	stdout, _ = captureStd(t)
	if err := printSessions(); err != nil {
		t.Fatal(err)
	}
	if got := stdout(); !strings.Contains(got, "[background: done") || !strings.Contains(got, "ago: Ran it.]") {
		t.Errorf("--sessions does not say how the run ended:\n%s", got)
	}
}

func TestLaunchStatusCarriesPendingAndMandate(t *testing.T) {
	prev := session.BgStatus{State: session.BgDone, Pending: &session.BgPending{Text: "q?"}, Mandate: &session.Mandate{Trust: "full"}}
	got := launchStatus(prev, time.Now())
	if got.State != session.BgRunning || got.Pending == nil || got.Pending.Text != "q?" || got.Mandate == nil {
		t.Fatalf("status = %+v", got)
	}
}
