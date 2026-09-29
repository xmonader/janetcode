package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// termAPI bridges a websocket to a shell on a pseudo-terminal in the
// session's directory — "drop into a terminal" without leaving the page.
// One shell per connection: when the socket closes the shell is reaped,
// so a fresh terminal never inherits a dead one's half-typed state.
func (s *server) termAPI(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell)
	cmd.Dir = s.cwd()
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		s.errf("terminal: %v", err)
		c.Close(websocket.StatusInternalError, "no shell")
		return
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		f.Close()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	send := func(v map[string]any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return false
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageText, data) == nil
	}

	// pty -> socket, until the shell exits or the socket dies.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 && !send(map[string]any{"type": "out", "data": string(buf[:n])}) {
				cancel()
				return
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// socket -> pty, through a writer of its own. A shell that stops
	// reading its input (a long foreground command under a big paste)
	// fills the pty, and a write there blocks until it reads again; done
	// on this goroutine, that kept the socket from being read, so a
	// closed tab never ended the handler and the shell leaked. The queue
	// is bounded and never waited on: input a stalled shell cannot take
	// is dropped, and closing the pty on the way out unblocks the writer.
	input := make(chan []byte, 64)
	defer close(input)
	go func() {
		for b := range input {
			if _, err := f.Write(b); err != nil {
				cancel()
				for range input {
				}
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Rows uint16 `json:"rows"`
			Cols uint16 `json:"cols"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "in":
			select {
			case input <- []byte(msg.Data):
			default:
			}
		case "resize":
			pty.Setsize(f, &pty.Winsize{Rows: msg.Rows, Cols: msg.Cols})
		}
	}
}
