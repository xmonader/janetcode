package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A server can break its own stream — a panic, a stray print on stdout, a
// frame past the size limit — and keep running. The reader stops, but
// nothing else notices: every query then waited out its whole deadline
// against a server that could no longer answer, and said only that it had
// not answered in time.
func TestAQueryFailsAtOnceWhenTheServersStreamBreaks(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--garbage-after-open"))
	defer m.Close()
	if err := os.WriteFile(filepath.Join(root, "a.foo"), []byte("fine\nthing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := m.Hover(context.Background(), "a.foo", Point{Line: 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("the query succeeded against a server whose stream had broken")
	}
	if !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("err = %v, want the reason the stream broke", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("waited %s for a server that could not answer", elapsed)
	}
}

// TestSendCtxAbandonsAWedgedWrite is the shutdown-path half of the
// wedged-server fix: a pipe write has no deadline, so a server that
// stopped reading stdin used to park send forever — including close()'s
// shutdown, which then never reached procgroup.Stop and kept the whole
// process alive. The context now bounds the wait.
func TestSendCtxAbandonsAWedgedWrite(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	c := &client{stdin: w}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = c.sendCtx(ctx, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "x", "params": strings.Repeat("p", 256<<10)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("send took %s to abandon; it ignored the context", elapsed)
	}
}

// reply and the method-not-supported answer are written from the read
// loop, and the read loop is the only thing draining the server's
// stdout. They used the unbounded send, so a server that stopped reading
// its stdin deadlocked the pair: the server blocked writing to a stdout
// nobody was draining, and the client blocked writing to a stdin nobody
// was reading. Neither ever returned, and close()'s shutdown could not
// get past it to kill the process.
func TestAReplyFromTheReadLoopCannotWedgeIt(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := &client{stdin: w}
	// Bigger than a pipe buffer, so the write blocks with nobody reading.
	big := strings.Repeat("x", 1<<20)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.reply(json.RawMessage(`1`), map[string]any{"text": big})
	}()
	select {
	case <-done:
	case <-time.After(replyTimeout + 15*time.Second):
		t.Fatal("reply never returned: the read loop is wedged on the server's stdin")
	}

	// The frame went out in part, so the stream is out of frame and every
	// later write is refused rather than spliced into the half-sent one.
	if c.broken == nil {
		t.Fatal("a partly written frame left the stream usable")
	}
	if err := c.sendCtx(context.Background(), map[string]any{"jsonrpc": "2.0", "id": 2}); !errors.Is(err, c.broken) {
		t.Fatalf("a write after a partial frame returned %v", err)
	}
}
