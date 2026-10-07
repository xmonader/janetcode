//go:build !windows

package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// ptyLog collects what the terminal side of a pty receives.
type ptyLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *ptyLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

func (l *ptyLog) waitFor(t *testing.T, s string, after int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := l.String()
		if i := strings.Index(out[after:], s); i >= 0 {
			return after + i + len(s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q; output so far:\n%s", s, l.String())
	return 0
}

// The screen-reader conversation, driven through a real pty: a request,
// a permission question answered by typing y, the answer, /quit. What the
// terminal receives is append-only — no escape sequence of any kind, no
// carriage return that is not the line discipline's own before a newline,
// no box drawing — and every state is said in words.
func TestConverseIsAppendOnlyUnderAPty(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pty:", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	log := &ptyLog{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				log.mu.Lock()
				log.buf.Write(b[:n])
				log.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x.txt","content":"1"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "## Done\nI wrote **x.txt** with `1` in it.\n\x1b[2J\x1b[31mred\x1b[0m\rover"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 3, OutputTokens: 4}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	intr := make(chan os.Signal)
	done := make(chan error, 1)
	go func() {
		done <- Converse(context.Background(), ConverseOptions{
			Options:    Options{Agent: ag, Session: sess, Stdin: tty, Stdout: tty, Cwd: root},
			Interrupts: intr,
		})
	}()

	at := log.waitFor(t, "Ready.", 0)
	at = log.waitFor(t, "> ", at)
	ptmx.WriteString("write x\n")
	at = log.waitFor(t, "Waiting for permission: write_file", at)
	at = log.waitFor(t, "Type y to allow once", at)
	at = log.waitFor(t, "> ", at)
	ptmx.WriteString("y\n")
	at = log.waitFor(t, "Allowed.", at)
	at = log.waitFor(t, "Turn finished.", at)
	log.waitFor(t, "Ready.", at)
	ptmx.WriteString("/quit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("/quit did not end the conversation")
	}
	log.waitFor(t, "Goodbye.", 0)

	out := log.String()
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\r") {
		t.Errorf("a bare carriage return (an overwrite) reached the terminal:\n%q", out)
	}
	if strings.ContainsAny(out, "─│┌┐└┘╭╮╰╯⠋⠙⠹") {
		t.Errorf("box drawing or a spinner frame reached the terminal:\n%s", out)
	}
	for _, want := range []string{"screen reader mode", "Working.", "Running tool: write_file", "Tool write_file finished.", "Answer:", "Done\r\n", "I wrote x.txt with 1 in it."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "x.txt")); err != nil || string(b) != "1" {
		t.Errorf("x.txt = %q, %v", b, err)
	}
}

// ctrl+c while a turn runs cancels that turn and the conversation goes on;
// at the prompt it ends the conversation.
func TestConverseInterruptCancelsTheTurnNotTheSession(t *testing.T) {
	block := &blocking{started: make(chan struct{}, 1)}
	ag := &agent.Agent{Provider: block, Tools: tool.NewRegistry(), Model: "m"}
	in, w, _ := os.Pipe()
	defer in.Close()
	var out syncBuf
	intr := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- Converse(context.Background(), ConverseOptions{
			Options:    Options{Agent: ag, Stdin: in, Stdout: &out, Cwd: t.TempDir()},
			Interrupts: intr,
		})
	}()
	w.WriteString("wait forever\n")
	<-block.started
	intr <- os.Interrupt
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Turn cancelled.") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "Turn cancelled.") {
		t.Fatalf("interrupt did not cancel the turn:\n%s", out.String())
	}
	select {
	case <-done:
		t.Fatal("the conversation ended with the turn")
	case <-time.After(100 * time.Millisecond):
	}
	intr <- os.Interrupt
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt at the prompt did not end the conversation")
	}
	w.Close()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// blocking is a provider whose call waits for its context.
type blocking struct{ started chan struct{} }

func (b *blocking) Name() string { return "blocking" }
func (b *blocking) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReadableDropsMarkup(t *testing.T) {
	cases := map[string]string{
		"## Heading":            "Heading",
		"a **bold** and `code`": "a bold and code",
		"---":                   "",
		"- item":                "- item",
		"1. first":              "1. first",
		"│ cell ─────── │":      "│ cell   │",
	}
	for in, want := range cases {
		if got := readable(in); got != want {
			t.Errorf("readable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScreenReaderWanted(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if w, _ := ScreenReaderWanted(env(nil)); w {
		t.Error("wanted with no signal")
	}
	if w, _ := ScreenReaderWanted(env(map[string]string{"JANETCODE_SCREEN_READER": "1"})); !w {
		t.Error("JANETCODE_SCREEN_READER=1 ignored")
	}
	if w, _ := ScreenReaderWanted(env(map[string]string{"ACCESSIBILITY_ENABLED": "1"})); !w {
		t.Error("ACCESSIBILITY_ENABLED=1 ignored")
	}
	if w, off := ScreenReaderWanted(env(map[string]string{"ACCESSIBILITY_ENABLED": "1", "JANETCODE_SCREEN_READER": "0"})); w || !off {
		t.Error("JANETCODE_SCREEN_READER=0 does not turn detection off")
	}
}
