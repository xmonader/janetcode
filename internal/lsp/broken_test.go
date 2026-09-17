package lsp

import (
	"context"
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
