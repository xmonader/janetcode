package llm

import (
	"context"
	"sync"
	"time"
)

// Pacer spaces out requests after a provider starts refusing them.
//
// Retry rescues one rate-limited call. It does nothing about the next one,
// which arrives just as fast and is refused just as quickly, so a long
// unsupervised run spends its time in backoff instead of working. The
// pacer is the other half: each refusal widens the gap between request
// starts, and a quiet spell narrows it again.
//
// It costs nothing until the first refusal — the gap starts at zero and a
// session that is never rate limited never waits.
type Pacer struct {
	// Base is the gap imposed by the first refusal. Zero means one second.
	Base time.Duration
	// Max bounds the gap however many refusals arrive. Zero means a minute.
	Max time.Duration
	// Ease is how much each success narrows the gap once the provider has
	// been quiet for Quiet. Zero means half a second.
	Ease time.Duration
	// Quiet is how long without a refusal before recovery begins. Zero
	// means a minute.
	Quiet time.Duration

	mu          sync.Mutex
	gap         time.Duration
	lastCall    time.Time
	lastRefusal time.Time
}

func (p *Pacer) settings() (base, max, ease, quiet time.Duration) {
	base, max, ease, quiet = p.Base, p.Max, p.Ease, p.Quiet
	if base == 0 {
		base = time.Second
	}
	if max == 0 {
		max = time.Minute
	}
	if ease == 0 {
		ease = 500 * time.Millisecond
	}
	if quiet == 0 {
		quiet = time.Minute
	}
	return
}

// Wait blocks until the next request may start. It returns early if the
// context is cancelled, so a user interrupting a paced session is not made
// to sit through the gap.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	gap := p.gap
	last := p.lastCall
	p.lastCall = time.Now()
	p.mu.Unlock()
	if gap <= 0 || last.IsZero() {
		return nil
	}
	due := last.Add(gap)
	d := time.Until(due)
	if d <= 0 {
		return nil
	}
	p.mu.Lock()
	p.lastCall = due // the next call is spaced from when this one may start
	p.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Refused widens the gap. Call it when a request came back rate limited.
func (p *Pacer) Refused() {
	base, max, _, _ := p.settings()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastRefusal = time.Now()
	if p.gap == 0 {
		p.gap = base
	} else if p.gap < max {
		p.gap *= 2
	}
	if p.gap > max {
		p.gap = max
	}
}

// Served narrows the gap, but only once the provider has been quiet for a
// while. A success that arrives moments after a refusal — the retry that
// finally landed — is not evidence that the limit has lifted, so it
// restarts the quiet period instead of counting as recovery.
func (p *Pacer) Served() {
	_, _, ease, quiet := p.settings()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gap == 0 {
		return
	}
	if time.Since(p.lastRefusal) < quiet {
		return
	}
	p.gap -= ease
	if p.gap < 0 {
		p.gap = 0
	}
}

// Gap is the current spacing, for reporting.
func (p *Pacer) Gap() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gap
}
