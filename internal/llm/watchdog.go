package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A streamed call can stop producing bytes without ever failing: the
// connection stays open, the provider never writes another frame, and the
// turn waits for as long as the process lives. Two bounds end it. The
// idle timeout is reset by every increment, so a slow model that keeps
// talking is never cut off; the lifetime cap is not, so a stream that
// dribbles one token a minute forever still ends. Both produce an error
// the agent reports as a failed turn, which the user can simply resend.

const (
	// DefaultStreamIdle is how long a stream may produce nothing.
	DefaultStreamIdle = 240 * time.Second
	// DefaultStreamMax is the longest one streamed call may take.
	DefaultStreamMax = 900 * time.Second
)

// StreamLimits bound one streamed call. A zero field means the default, a
// negative one means no bound.
type StreamLimits struct {
	Idle time.Duration
	Max  time.Duration
}

// Resolve fills in the defaults and turns "disabled" into zero.
func (l StreamLimits) Resolve() StreamLimits {
	switch {
	case l.Idle == 0:
		l.Idle = DefaultStreamIdle
	case l.Idle < 0:
		l.Idle = 0
	}
	switch {
	case l.Max == 0:
		l.Max = DefaultStreamMax
	case l.Max < 0:
		l.Max = 0
	}
	return l
}

// ErrStreamWatchdog matches any error a stream watchdog produced, so a
// caller can recognise one without knowing which bound fired.
var ErrStreamWatchdog = errors.New("llm: stream watchdog")

// StreamStalled is the error a watchdog produces. Lifetime says the
// non-resettable cap fired rather than the idle timeout.
type StreamStalled struct {
	Limit    time.Duration
	Lifetime bool
}

func (e *StreamStalled) Error() string {
	if e.Lifetime {
		return fmt.Sprintf("stream stalled: one request may take %s (stream_max_duration)", e.Limit)
	}
	return fmt.Sprintf("stream stalled: nothing received for %s (stream_idle_timeout)", e.Limit)
}

// Is makes errors.Is(err, ErrStreamWatchdog) true for both bounds.
func (e *StreamStalled) Is(target error) bool { return target == ErrStreamWatchdog }

// Watchdog cancels a streamed call that stops producing.
type Watchdog struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	mu    sync.Mutex
	last  time.Time
	fired error
}

// Watch derives a context cancelled when either bound is exceeded. Call
// Tick for every increment received and Stop when the call is over; Wrap
// turns the cancellation into the watchdog's own error.
func Watch(ctx context.Context, lim StreamLimits) (context.Context, *Watchdog) {
	return WatchSince(ctx, lim, time.Now())
}

// WatchSince is Watch with the lifetime cap measured from an earlier
// moment. A provider that retries a stream which died without a terminal
// frame builds a watchdog per attempt, because the idle bound has to
// start again — a fresh attempt really is producing. The lifetime cap
// must not: it bounds the call, it is the thing that stops a stream
// dribbling one token a minute forever, and restarting it per attempt
// multiplied it by the retry count.
func WatchSince(ctx context.Context, lim StreamLimits, start time.Time) (context.Context, *Watchdog) {
	lim = lim.Resolve()
	ctx, cancel := context.WithCancel(ctx)
	w := &Watchdog{cancel: cancel, done: make(chan struct{}), last: time.Now()}
	if lim.Idle <= 0 && lim.Max <= 0 {
		return ctx, w
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.watch(start, lim)
	}()
	return ctx, w
}

// expired evaluates both bounds against now, recording the first that
// fired, and reports whether one has.
func (w *Watchdog) expired(start, now time.Time, lim StreamLimits) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.fired != nil:
	case lim.Max > 0 && now.Sub(start) >= lim.Max:
		w.fired = &StreamStalled{Limit: lim.Max, Lifetime: true}
	case lim.Idle > 0 && now.Sub(w.last) >= lim.Idle:
		w.fired = &StreamStalled{Limit: lim.Idle}
	}
	return w.fired != nil
}

func (w *Watchdog) watch(start time.Time, lim StreamLimits) {
	// An attempt that begins with the call's lifetime budget already
	// spent is over before its first tick.
	if w.expired(start, time.Now(), lim) {
		w.cancel()
		return
	}
	every := lim.Idle
	if lim.Max > 0 && (every <= 0 || lim.Max < every) {
		every = lim.Max
	}
	every /= 10
	if every < 5*time.Millisecond {
		every = 5 * time.Millisecond
	}
	if every > time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case now := <-t.C:
			if w.expired(start, now, lim) {
				w.cancel()
				return
			}
		}
	}
}

// Tick records that the stream produced something, resetting the idle
// timeout. It is safe on a nil watchdog.
func (w *Watchdog) Tick() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.last = time.Now()
	w.mu.Unlock()
}

// Expect tells the watchdog that the caller is about to wait d on
// purpose — a retry backoff, a pacing gap — so the idle bound measures
// the silence after that wait instead of firing in the middle of it. The
// idle bound is there to catch a stream that stopped talking, not a wait
// the harness chose. The lifetime bound is not moved: a call that spends
// its whole budget in backoff has still spent it.
func (w *Watchdog) Expect(d time.Duration) {
	if w == nil || d <= 0 {
		return
	}
	w.mu.Lock()
	if until := time.Now().Add(d); until.After(w.last) {
		w.last = until
	}
	w.mu.Unlock()
}

// Stop releases the watchdog and its context, and waits for the watching
// goroutine to be done with it — so Err, read after Stop, cannot race a
// bound firing at the same moment and report a stall that did not cause
// the failure.
func (w *Watchdog) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
	w.wg.Wait()
	w.cancel()
}

// LifetimeStalled reports whether err is the non-resettable lifetime cap
// rather than the idle timeout. The lifetime cap is the bound on the
// whole call: a caller that retries a dead stream must not retry this.
func LifetimeStalled(err error) bool {
	var ss *StreamStalled
	return errors.As(err, &ss) && ss.Lifetime
}

// Err is the watchdog's error when a bound fired, else nil.
func (w *Watchdog) Err() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// Wrap replaces the cancellation a provider saw with the reason for it.
// An error from anything but the watchdog is returned unchanged.
func (w *Watchdog) Wrap(err error) error {
	if err == nil {
		return nil
	}
	if fired := w.Err(); fired != nil {
		return fired
	}
	return err
}
