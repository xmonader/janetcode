// Package pipewrite writes a whole message to a child process's pipe
// under a context, and says exactly what reached the pipe when the
// context ends first.
//
// A pipe write has no deadline of its own, so bounding one by running it
// on a goroutine and returning when the context is done leaves the write
// parked: it completes later, when the child next drains its stdin. The
// caller has already been told the write failed, and the child acts on
// the message anyway — a cancelled tool call that still runs, a retry
// that interleaves two payloads on one pipe, a partial line spliced into
// the next command on a live shell.
//
// An exec pipe is pollable, so its write deadline can be pushed into the
// past to unblock it. The write then happens on the calling goroutine and
// the byte count is the truth about what the child received.
package pipewrite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// deadlineWriter is a pipe that can bound its own write. os/exec's
// StdinPipe returns one: it wraps an *os.File, and a pipe is pollable.
type deadlineWriter interface {
	io.Writer
	SetWriteDeadline(time.Time) error
}

// ErrAbandoned marks a write the context ended. errors.Is reports it for
// both the clean and the partial case, so a caller that only wants to
// know "did this message get through" can ask that alone.
var ErrAbandoned = errors.New("pipewrite: write abandoned")

// PartialError is an abandoned write that had already delivered bytes, or
// one whose delivery cannot be known. The pipe is no longer usable: the
// child has the front of a message and will read whatever comes next as
// the rest of it, so the caller must close or kill it rather than write
// again.
type PartialError struct {
	Written, Total int
	// Indeterminate says the write could not be bounded at all, so
	// whether anything arrived is unknown. Unknown is treated as partial:
	// that is the only safe direction.
	Indeterminate bool
	Cause         error
}

func (e *PartialError) Error() string {
	if e.Indeterminate {
		return fmt.Sprintf("pipewrite: write abandoned with %d bytes unaccounted for; the pipe cannot be reused", e.Total)
	}
	return fmt.Sprintf("pipewrite: write abandoned after %d of %d bytes; the pipe cannot be reused", e.Written, e.Total)
}

func (e *PartialError) Unwrap() error { return e.Cause }
func (e *PartialError) Is(target error) bool {
	return target == ErrAbandoned
}

// cleanAbandon is an abandoned write that delivered nothing, so the child
// never saw the message and the pipe is still good.
type cleanAbandon struct{ cause error }

func (e *cleanAbandon) Error() string {
	return "pipewrite: write abandoned before any byte reached the pipe: " + e.cause.Error()
}
func (e *cleanAbandon) Unwrap() error        { return e.cause }
func (e *cleanAbandon) Is(target error) bool { return target == ErrAbandoned }

// Write writes all of p to w, bounded by ctx.
//
// A nil error means the child has the whole message. An error that
// errors.Is(err, ErrAbandoned) means the context ended first; if it is
// also a *PartialError the pipe must not be used again. Any other error
// is the pipe's own.
func Write(ctx context.Context, w io.Writer, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, &cleanAbandon{cause: err}
	}
	dw, ok := w.(deadlineWriter)
	if !ok {
		return writeUnbounded(ctx, w, p)
	}
	// Clear whatever a previous abandoned write left behind, then unblock
	// this one the moment ctx is done.
	if err := dw.SetWriteDeadline(time.Time{}); err != nil {
		return writeUnbounded(ctx, w, p)
	}
	stop := context.AfterFunc(ctx, func() { _ = dw.SetWriteDeadline(time.Now()) })
	defer stop()
	n, err := dw.Write(p)
	_ = dw.SetWriteDeadline(time.Time{})
	switch {
	case err == nil && n == len(p):
		return n, nil
	case err == nil:
		return n, io.ErrShortWrite
	case ctx.Err() == nil:
		return n, err // the pipe's own failure, not an abandonment
	case n == 0:
		return 0, &cleanAbandon{cause: ctx.Err()}
	default:
		return n, &PartialError{Written: n, Total: len(p), Cause: ctx.Err()}
	}
}

// writeUnbounded is the fallback for a writer with no deadline: the write
// runs on a goroutine, so an abandoned one cannot be accounted for and is
// reported as indeterminate, which the caller must treat as partial.
func writeUnbounded(ctx context.Context, w io.Writer, p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := w.Write(p)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		if r.err == nil && r.n != len(p) {
			return r.n, io.ErrShortWrite
		}
		return r.n, r.err
	case <-ctx.Done():
		return 0, &PartialError{Total: len(p), Indeterminate: true, Cause: ctx.Err()}
	}
}
