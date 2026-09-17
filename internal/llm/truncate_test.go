package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestTruncateMarksAndUnwraps(t *testing.T) {
	plain := errors.New("connection reset")
	marked := Truncate(fmt.Errorf("openai: stream read: %w", plain))
	if !Truncated(marked) {
		t.Fatal("marked error not recognized")
	}
	if !errors.Is(marked, plain) {
		t.Fatal("cause lost")
	}
	if Truncated(plain) {
		t.Fatal("plain error marked")
	}
	if Truncated(nil) {
		t.Fatal("nil marked")
	}
}

func TestRetryableTruncationAndStalls(t *testing.T) {
	if !Retryable(Truncate(errors.New("unexpected EOF"))) {
		t.Fatal("a truncated stream is worth a fresh attempt")
	}
	// The watchdog's stall-kill surfaces from the transport as a context
	// cancellation, but it is not the user's will — retryable.
	if !Retryable(&StreamStalled{Limit: 1}) {
		t.Fatal("stall-kill not retryable")
	}
	// The user's own cancellation is not.
	if Retryable(context.Canceled) || Retryable(context.DeadlineExceeded) {
		t.Fatal("user cancellation retried")
	}
	// A status the policy rejects stays final even when marked truncated.
	if Retryable(Truncate(&fakeStatus{status: http.StatusBadRequest})) {
		t.Fatal("a 400 became retryable via truncation")
	}
	if !Retryable(Truncate(&fakeStatus{status: http.StatusServiceUnavailable})) {
		t.Fatal("a truncated 503 lost its retryability")
	}
	if RetryReason(&StreamStalled{Limit: 1}) != "stream stalled" {
		t.Fatalf("reason = %q", RetryReason(&StreamStalled{Limit: 1}))
	}
	if RetryReason(Truncate(errors.New("x"))) != "stream cut off" {
		t.Fatalf("reason = %q", RetryReason(Truncate(errors.New("x"))))
	}
}
