package acp

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// A response for a call that will take no more (a duplicate, or one that
// lands while its caller is leaving on a cancelled context) is dropped.
// Handing it over blocked the read loop on a channel nobody would read
// again, and every frame after it, a session/cancel included, went
// unread for the life of the connection.
func TestADuplicateResponseDoesNotStallTheReadLoop(t *testing.T) {
	c := newConn(io.Discard)
	// A call that is still registered and has had its answer: its
	// one-slot channel is full and nothing reads it.
	c.pend["1"] = make(chan message, 1)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
		`{"jsonrpc":"2.0","method":"ping"}` + "\n")
	handled := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.readLoop(context.Background(), in, func(_ context.Context, m message) { handled <- m.Method })
	}()
	select {
	case got := <-handled:
		if got != "ping" {
			t.Fatalf("handled %q, want ping", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the frame after a duplicate response was never read")
	}
	<-done
}
