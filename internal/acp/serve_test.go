package acp

import (
	"context"
	"io"
	"testing"
	"time"
)

// Serve's own comment says it reads until the input ends or ctx is
// cancelled, but the loop only ever waited for the input: the scanner
// blocks in Read, and a SIGTERM (which cancels ctx) left `janetcode
// --acp` running until the editor happened to close its stdin.
func TestServeReturnsWhenTheContextEndsWithTheInputStillOpen(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()
	srv := New(Options{In: inR, Out: io.Discard, Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	cancel()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve ignored the cancelled context and kept waiting on its input")
	}
}
