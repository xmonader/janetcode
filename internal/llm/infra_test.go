package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

type statusErr int

func (s statusErr) Error() string            { return fmt.Sprintf("HTTP %d", int(s)) }
func (s statusErr) HTTPStatus() int          { return int(s) }
func (s statusErr) RetryHint() time.Duration { return 0 }

func TestInfrastructure(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{statusErr(429), true},
		{statusErr(504), true},
		{statusErr(500), true},
		{statusErr(402), true},
		{statusErr(401), true},
		{statusErr(400), false},
		{statusErr(404), false},
		{fmt.Errorf("run: %w", statusErr(503)), true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{fmt.Errorf("stalled: %w", ErrStreamWatchdog), true},
		{Truncate(errors.New("openai: stream ended without finish_reason")), true},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{errors.New("turn hit its model-call cap"), false},
		{nil, false},
	} {
		if got := Infrastructure(c.err); got != c.want {
			t.Errorf("Infrastructure(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type msgErr struct {
	statusErr
	msg string
}

func (m msgErr) Error() string { return m.msg }

func TestToolCallRejected(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want bool
	}{
		{"openrouter stream error: Upstream error from Groq: Tool call validation failed: parameters for tool apply_patch (HTTP 500)", true},
		{"Failed to parse tool call arguments as JSON", true},
		{"Parsing failed. The model generated output that could not be parsed.", true},
		{"see 'failed_generation' for more details", true},
		{"Invalid Tool Call", true},
		{"Upstream idle timeout exceeded", false},
	} {
		err := InStream(msgErr{statusErr(500), c.msg})
		if got := IsToolCallRejected(err); got != c.want {
			t.Errorf("%q: IsToolCallRejected = %v", c.msg, got)
		}
		if c.want {
			if Retryable(err) {
				t.Errorf("%q: must not be retried", c.msg)
			}
			w := AsToolCallRejected(err)
			if !errors.Is(w, ErrToolCallRejected) || w.Error() != c.msg {
				t.Errorf("%q: wrapped = %v", c.msg, w)
			}
		} else if !Retryable(err) {
			t.Errorf("%q: mid-stream 500 should stay retryable", c.msg)
		}
	}
}
