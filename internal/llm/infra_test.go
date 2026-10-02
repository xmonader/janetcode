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
