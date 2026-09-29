package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A shell that stops reading its input (a long foreground command, a
// big paste) fills the pty's input buffer. Closing the socket must still
// end the handler and reap the shell; a write stuck on the pty used to
// keep the handler from ever reading the socket again, leaking the
// shell and both goroutines.
func TestTerminalHandlerEndsWhenTheShellStopsReading(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "deaf.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 1000\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", script)
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(context.Background(), Options{Agent: &agent.Agent{Tools: tool.NewRegistry()}, Session: sess, Cwd: root})
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.termAPI(w, r)
		close(done)
	}))
	defer ts.Close()

	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/term", nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := json.Marshal(map[string]any{"type": "in", "data": strings.Repeat(strings.Repeat("x", 63)+"\n", 256)})
	// Far more than a pty's input buffer holds; nothing reads it.
	for i := 0; i < 64; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := ws.Write(ctx, websocket.MessageText, chunk)
		cancel()
		if err != nil {
			break
		}
	}
	ws.CloseNow()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal handler is still running after its socket closed")
	}
}
