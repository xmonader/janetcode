package pipewrite

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestWriteDeliversNothingWhenAbandoned is the whole point: a write the
// context ended must not arrive later. Running it on a goroutine and
// returning on ctx.Done left it parked, so the reader received the
// payload after the caller had been told the write failed.
func TestWriteDeliversNothingWhenAbandoned(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// Fill the pipe so the next write must block.
	big := make([]byte, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Write(ctx, w, big); !errors.Is(err, ErrAbandoned) {
		t.Fatalf("err = %v, want an abandonment", err)
	}
	// Whatever the pipe buffer took is all the reader can ever see: the
	// write is over, not parked. Drain it and make sure it stops.
	if err := r.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(big))
	n, err := io.ReadFull(r, buf)
	if err == nil {
		t.Fatalf("the reader got all %d bytes of an abandoned write", n)
	}
	if n >= len(big) {
		t.Fatalf("the abandoned write was delivered in full (%d bytes)", n)
	}
}

// TestAbandonedWriteReportsWhetherThePipeIsStillUsable: the caller has to
// know the difference. Nothing delivered means the message never
// happened and the pipe is good; a partial frame means the reader holds
// the front of a message and will read whatever comes next as the rest.
func TestAbandonedWriteReportsWhetherThePipeIsStillUsable(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	// Already cancelled: nothing can have been written.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Write(ctx, w, []byte("hello\n"))
	var partial *PartialError
	if !errors.Is(err, ErrAbandoned) || errors.As(err, &partial) {
		t.Fatalf("a write that never started reported %v, want a clean abandonment", err)
	}

	// Blocked partway: the pipe is unusable.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := Write(ctx2, w, make([]byte, 1<<20)); !errors.As(err, &partial) {
		t.Fatalf("a write that filled the pipe and stopped reported %v, want a partial", err)
	} else if partial.Written == 0 || partial.Written >= partial.Total {
		t.Fatalf("partial = %+v", partial)
	}
}

// TestWriteSucceedsWhenTheReaderKeepsUp: the bound must not change the
// ordinary case.
func TestWriteSucceedsWhenTheReaderKeepsUp(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	go io.Copy(io.Discard, r)
	payload := make([]byte, 4<<20)
	n, err := Write(context.Background(), w, payload)
	if err != nil || n != len(payload) {
		t.Fatalf("n = %d err = %v", n, err)
	}
	// And the pipe is still good for the next one: an earlier deadline
	// must not linger.
	if n, err := Write(context.Background(), w, []byte("again\n")); err != nil || n != 6 {
		t.Fatalf("second write: n = %d err = %v", n, err)
	}
}
