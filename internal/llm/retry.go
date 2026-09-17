package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Every provider has to survive the same few failures — a rate limit, an
// overloaded gateway, a connection that never opened — and a harness that
// answers them differently depending on which backend it is talking to is
// a harness whose behaviour nobody can describe. This is the one policy:
// what is worth another attempt, how long to wait, what the user is told
// while it waits, and what is left behind afterwards. A provider supplies
// only the part that is genuinely its own, the mapping from its wire
// error to an HTTP status and a Retry-After (see StatusError).
//
// It deliberately lives above the SDKs. Retries hidden inside a vendored
// client are invisible to the stream watchdog and to the events a UI
// draws, so a long Retry-After there reads as a stalled turn with no
// countdown; a provider that has such a knob turns it off and uses this.

// StatusError is a provider error that carries an HTTP status and the
// server's own Retry-After, when it gave one. Both providers' API error
// types implement it, which is what lets one policy judge them.
type StatusError interface {
	error
	// HTTPStatus is the response status, or 0 when there was none.
	HTTPStatus() int
	// RetryHint is how long the server asked the caller to wait, or 0.
	RetryHint() time.Duration
}

// MaxRetryAfter is the longest a provider may ask the harness to wait.
const MaxRetryAfter = 2 * time.Minute

// ParseRetryAfter reads a Retry-After header in either form the RFC
// allows, seconds or an HTTP date, and ignores anything longer than a
// couple of minutes: a server asking for an hour is telling the user to
// come back later, not telling the harness to sleep through it.
func ParseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(v); err == nil {
		d = time.Until(when)
	}
	if d <= 0 || d > MaxRetryAfter {
		return 0
	}
	return d
}

// RetryAfterFrom reads the Retry-After of a response the caller still
// holds, for the statuses where it means "come back": a 429 or a 503 is
// a window that reopens, anything else is a failure that will not clear.
func RetryAfterFrom(h http.Header, status int) time.Duration {
	if status != 429 && status != 503 {
		return 0
	}
	return ParseRetryAfter(h.Get("Retry-After"))
}

// exhausted are the ways a provider says the account has run out, rather
// than that this request arrived too fast. Both come back as 429, and
// retrying the first only delays a failure that was never going to clear —
// with the real reason buried under the retries.
var exhausted = []string{
	"insufficient_quota",
	"quota exceeded",
	"exceeded your current quota",
	"available balance",
	"insufficient balance",
	"out of budget",
	"billing",
	"usage limit reached",
	"credit balance is too low",
}

// PermanentLimit reports whether a rate-limit message is really an
// exhausted account.
func PermanentLimit(msg string) bool {
	msg = strings.ToLower(msg)
	for _, needle := range exhausted {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// Retryable reports whether an error is worth another attempt.
//
// 529 is Anthropic's "overloaded", which gateways pass through. 500 and
// 501 are not retried: a plain internal error is the server saying this
// request broke it, and sending it again breaks it again — with the real
// reason buried under the retries.
func Retryable(err error) bool {
	var se StatusError
	if errors.As(err, &se) {
		// ClassifyText, not se.Error(): the display copy is cut at 300
		// runes, and a verbose gateway puts its trace ids first, so the
		// word that says the account is finished can sit past the cut.
		if PermanentLimit(ClassifyText(err)) {
			return false
		}
		switch se.HTTPStatus() {
		case 429, 408, 529, 502, 503, 504:
			return true
		}
		return false
	}
	// A stall-kill reads as a context cancellation from the transport,
	// but it is the harness giving up on a silent server — the user is
	// still there and the request never answered, so a fresh attempt is
	// exactly what they would ask for.
	if errors.Is(err, ErrStreamWatchdog) {
		return true
	}
	// Connection-level failures.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// RetryReason names why a call is being retried, in the few words the
// status line has room for.
func RetryReason(err error) string {
	var se StatusError
	if errors.As(err, &se) {
		switch s := se.HTTPStatus(); {
		case s == 429:
			return "rate limited"
		case s >= 500:
			return fmt.Sprintf("server error %d", s)
		case s > 0:
			return fmt.Sprintf("http %d", s)
		}
	}
	if errors.Is(err, ErrStreamWatchdog) {
		return "stream stalled"
	}
	if Truncated(err) {
		return "stream cut off"
	}
	return "connection failed"
}

// Truncated reports whether err is a stream that ended before any
// terminal frame: the connection dropped, the body ended clean but
// early, a chunk arrived malformed. The request never produced an
// answer, so the harness may resend it as one fresh request over the
// intact history — never by splicing the partial output back.
func Truncated(err error) bool {
	var t *truncatedStream
	return errors.As(err, &t)
}

// Truncate marks err as a stream that died without a terminal frame.
type truncatedStream struct{ err error }

func (e *truncatedStream) Error() string { return e.err.Error() }
func (e *truncatedStream) Unwrap() error { return e.err }

// Truncate marks err as a stream that died without a terminal frame.
func Truncate(err error) error { return &truncatedStream{err} }

// ResetWatchdog points the retrier at the watchdog of a fresh attempt:
// a stream that died and is being retried runs under a new one, and the
// backoff waits inside it must be expected there.
func (r *Retrier) ResetWatchdog(wd *Watchdog) { r.wd = wd }

// RetryBase is the first backoff; it doubles with every attempt.
const RetryBase = 500 * time.Millisecond

// Retrier carries one call's retry budget: how many attempts are left,
// what the user has been told, and what the call had to survive. A
// provider makes one per streamed call, asks Wait about every failure,
// and calls Landed when one finally answers.
//
// An attempt only counts when Wait is asked about it, so a provider that
// resends for a reason of its own — a parameter the server rejects, a key
// it rotates away from — simply loops without consulting the Retrier and
// spends nothing.
type Retrier struct {
	max  int
	emit func(Event)
	wd   *Watchdog

	spent int
	sum   Retry
}

// NewRetrier makes a Retrier for one call. emit may be nil; wd may be nil.
func NewRetrier(max int, emit func(Event), wd *Watchdog) *Retrier {
	if emit == nil {
		emit = func(Event) {}
	}
	if max < 0 {
		max = 0
	}
	return &Retrier{max: max, emit: emit, wd: wd}
}

// Wait decides what to do about a failed attempt. It returns nil when the
// caller should try again, having already waited out the backoff; it
// returns the error to fail with otherwise. The wait honours ctx, the
// server's own Retry-After where it gave one, and tells the watchdog to
// expect it so a deliberate pause does not read as a stalled stream.
func (r *Retrier) Wait(ctx context.Context, err error) error {
	if r.spent >= r.max || !Retryable(err) || ctx.Err() != nil {
		return err
	}
	wait := RetryBase << r.spent
	// The server's own Retry-After wins where it gave one: it knows when
	// its window resets, and the backoff is only a guess.
	var se StatusError
	if errors.As(err, &se) && se.RetryHint() > 0 {
		wait = se.RetryHint()
	}
	r.spent++
	reason := RetryReason(err)
	r.sum.Attempts, r.sum.Reason, r.sum.Wait = r.spent, reason, r.sum.Wait+wait
	r.emit(Event{Type: EventStatus, Text: fmt.Sprintf("retrying %d/%d · %s · in %s", r.spent, r.max, reason, wait.Round(time.Second/10))})
	r.wd.Expect(wait)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	return nil
}

// Landed reports a call that failed, was retried and then succeeded. A
// status line is gone as soon as the next one replaces it, so without
// this a turn that took four attempts is indistinguishable afterwards
// from one that took none. A call that never failed says nothing.
func (r *Retrier) Landed() {
	if r.sum.Attempts == 0 {
		return
	}
	sum := r.sum
	r.emit(Event{Type: EventRetried, Retry: &sum})
}
