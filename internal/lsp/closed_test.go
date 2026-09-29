package lsp

import (
	"bufio"
	"os"
	"testing"
)

// cmd.Wait closes the stdout pipe once the server exits, which can land
// while the reader is still reading it. That read fails with "file already
// closed" rather than EOF, and it used to be recorded as the reason the
// server stopped — winning over the exit status and stderr the reaper
// reports a moment later.
func TestAClosedStdoutIsTheServerExitingNotAStreamError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r.Close()
	c := &client{pend: map[int64]chan rpcResponse{}, done: make(chan struct{})}
	c.read(bufio.NewReader(r))
	if c.err != nil {
		t.Fatalf("a closed stdout was reported as the failure: %v", c.err)
	}
}
