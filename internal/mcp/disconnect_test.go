package mcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// stubTransport accepts every send and never answers, so a test can
// control exactly how the connection ends.
type stubTransport struct{ sent chan rpcMessage }

func (s stubTransport) send(_ context.Context, m rpcMessage) error { s.sent <- m; return nil }
func (s stubTransport) close()                                     {}
func (s stubTransport) hint() string                               { return "" }

func newStubClient() (*Client, stubTransport) {
	st := stubTransport{sent: make(chan rpcMessage, 8)}
	c := &Client{Name: "stub", pending: map[int64]chan rpcReply{}, done: make(chan struct{})}
	c.tr = st
	return c, st
}

// errReader fails the way a pipe does when the far side dies mid-stream,
// and the way bufio.Scanner reports a frame larger than its buffer.
type errReader struct{ err error }

func (r *errReader) Read(p []byte) (int, error) { return 0, r.err }

// A read loop that dies leaves the process running, so nothing else
// notices: every call in flight used to wait out its own timeout, and
// every later call did the same, against a connection that could no
// longer answer.
func TestAFailedReadLoopFailsCallsInsteadOfHangingThem(t *testing.T) {
	c, st := newStubClient()
	got := make(chan error, 1)
	go func() {
		_, err := c.call(context.Background(), "tools/call", map[string]any{})
		got <- err
	}()
	select {
	case <-st.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was never sent")
	}

	tr := &stdioTransport{c: c}
	tr.readLoop(&errReader{err: bufio.ErrTooLong})

	select {
	case err := <-got:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("call in flight = %v, want an ErrDisconnected", err)
		}
		if !strings.Contains(err.Error(), bufio.ErrTooLong.Error()) {
			t.Fatalf("call in flight = %v, want the reason the stream ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call in flight was never woken")
	}

	// And a call made afterwards fails for the same reason rather than
	// registering against a connection nothing will ever answer.
	if _, err := c.call(context.Background(), "tools/call", map[string]any{}); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("call after the stream died = %v, want an ErrDisconnected", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done was not closed")
	}
}

// A clean EOF is the server exiting, which the process reaper reports; the
// read loop must not invent a second, different reason for it.
func TestACleanEndOfStreamIsNotAFailure(t *testing.T) {
	c, _ := newStubClient()
	tr := &stdioTransport{c: c}
	tr.readLoop(&errReader{err: io.EOF})
	select {
	case <-c.Done():
		t.Fatal("a clean EOF ended the connection before the reaper reported why")
	default:
	}
}

// Close and the process reaper race; whichever wins, the reason a caller
// is given has to be readable rather than a torn read of a field.
func TestCloseAndExitAgreeOnOneReason(t *testing.T) {
	c, _ := newStubClient()
	c.exited(errors.New("signal: killed"))
	c.Close()
	err := c.exitError()
	if !errors.Is(err, ErrDisconnected) || !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("exitError = %v, want the first reason", err)
	}
}
