package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type fakeStatus struct {
	status int
	hint   time.Duration
	msg    string
}

func (e *fakeStatus) Error() string            { return e.msg }
func (e *fakeStatus) HTTPStatus() int          { return e.status }
func (e *fakeStatus) RetryHint() time.Duration { return e.hint }
func status(s int, msg string) *fakeStatus     { return &fakeStatus{status: s, msg: msg} }
func hinted(s int, d time.Duration) *fakeStatus {
	return &fakeStatus{status: s, hint: d, msg: "slow down"}
}
func eventsOf(t *testing.T) (func(Event), *[]Event) {
	t.Helper()
	var got []Event
	return func(e Event) { got = append(got, e) }, &got
}

func TestRetryableJudgesStatusAndExhaustion(t *testing.T) {
	for _, s := range []int{429, 408, 529, 502, 503, 504} {
		if !Retryable(status(s, "x")) {
			t.Errorf("%d should be retried", s)
		}
	}
	for _, s := range []int{500, 501, 400, 404, 422} {
		if Retryable(status(s, "x")) {
			t.Errorf("%d should not be retried", s)
		}
	}
	// An exhausted account comes back as 429 and is never going to clear.
	if Retryable(status(429, "You exceeded your current quota")) {
		t.Error("an exhausted quota should not be retried")
	}
	if Retryable(context.Canceled) {
		t.Error("a cancelled call should not be retried")
	}
	if !Retryable(errors.New("dial tcp: connection refused")) {
		t.Error("a connection failure should be retried")
	}
}

func TestRetryReasonNamesTheFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{status(429, "x"), "rate limited"},
		{status(503, "x"), "server error 503"},
		{status(404, "x"), "http 404"},
		{errors.New("boom"), "connection failed"},
	} {
		if got := RetryReason(tc.err); got != tc.want {
			t.Errorf("RetryReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestRetryAfterFromOnlyWhereItMeansComeBack(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "1")
	if got := RetryAfterFrom(h, 429); got != time.Second {
		t.Errorf("429 = %v", got)
	}
	if got := RetryAfterFrom(h, 503); got != time.Second {
		t.Errorf("503 = %v", got)
	}
	for _, s := range []int{400, 500, 502} {
		if got := RetryAfterFrom(h, s); got != 0 {
			t.Errorf("%d = %v, want it ignored", s, got)
		}
	}
}

// The budget is spent only on failures the Retrier is asked about, so a
// provider that resends for a reason of its own (a dialect quirk, a key
// rotation) does not eat into it.
func TestRetrierSpendsItsBudgetAndReportsIt(t *testing.T) {
	emit, got := eventsOf(t)
	rt := NewRetrier(2, emit, nil)
	err := status(429, "slow down")
	for i := 0; i < 2; i++ {
		if werr := rt.Wait(context.Background(), err); werr != nil {
			t.Fatalf("attempt %d: %v", i, werr)
		}
	}
	if werr := rt.Wait(context.Background(), err); werr == nil {
		t.Fatal("a third attempt should have been refused")
	}
	rt.Landed()
	var statuses, retried int
	for _, e := range *got {
		switch e.Type {
		case EventStatus:
			statuses++
		case EventRetried:
			retried++
			if e.Retry == nil || e.Retry.Attempts != 2 || e.Retry.Reason != "rate limited" || e.Retry.Wait <= 0 {
				t.Errorf("retried event = %+v", e.Retry)
			}
		}
	}
	if statuses != 2 || retried != 1 {
		t.Fatalf("%d status events and %d retried events, want 2 and 1", statuses, retried)
	}
}

func TestRetrierSaysNothingAboutACleanCall(t *testing.T) {
	emit, got := eventsOf(t)
	NewRetrier(3, emit, nil).Landed()
	if len(*got) != 0 {
		t.Fatalf("events = %+v", *got)
	}
}

func TestRetrierHonoursTheServersRetryAfter(t *testing.T) {
	rt := NewRetrier(1, nil, nil)
	start := time.Now()
	if err := rt.Wait(context.Background(), hinted(429, 120*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > time.Second {
		t.Fatalf("waited %v, want the server's 120ms rather than the 500ms guess", waited)
	}
}

func TestRetrierStopsWaitingWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := NewRetrier(3, nil, nil)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := rt.Wait(ctx, hinted(429, 10*time.Second))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("waited %v after cancellation", waited)
	}
}

// A wait the harness imposed on itself is not a stalled stream: the idle
// bound has to be pushed out by it, or a long Retry-After trips the
// watchdog halfway through a backoff nobody can see.
func TestExpectKeepsTheIdleBoundFromFiringDuringABackoff(t *testing.T) {
	ctx, wd := Watch(context.Background(), StreamLimits{Idle: 60 * time.Millisecond})
	defer wd.Stop()
	wd.Expect(300 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("the idle bound fired during a wait the watchdog was told to expect")
	case <-time.After(150 * time.Millisecond):
	}
	if err := wd.Err(); err != nil {
		t.Fatalf("watchdog fired: %v", err)
	}
	// It is a pause, not a reprieve: once the expected wait is over the
	// idle bound applies again.
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the idle bound never fired after the expected wait")
	}
	var stalled *StreamStalled
	if err := wd.Err(); !errors.As(err, &stalled) || stalled.Lifetime {
		t.Fatalf("watchdog error = %v, want the idle bound", err)
	}
}
