package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// A request that has not produced its first token by the time almost
// every request before it had is, more often than not, a request that a
// gateway has lost: the ai02 broker's "504 no reply received over
// mycelium" answers nothing for a minute and then fails, and a retry on
// the same route waits the same minute again. Hedging sends a duplicate
// to a second route at that moment and keeps whichever answers first
// ("The Tail at Scale"). The moment is the primary's own p95 time to
// first token, learned from the calls it has served, so about one call in
// twenty is duplicated and the rest cost nothing.
//
// It is opt-in (config hedge_provider): every hedge is a second request
// the user pays for, on a second key or route the user named.

const (
	// DefaultHedgeFallback is the hedge delay before enough first tokens
	// have been seen to learn one.
	DefaultHedgeFallback = 10 * time.Second
	// DefaultHedgeFloor is the shortest delay a learned p95 may give: a
	// fast provider's p95 of 400ms would duplicate every call that hit
	// a cold cache.
	DefaultHedgeFloor = 1500 * time.Millisecond
	// hedgeMinSamples is how many first tokens a p95 is learned from.
	hedgeMinSamples = 20
	// hedgeWindow is how many recent first tokens are kept: the p95 of
	// the last couple of hundred calls, not of the session's whole life,
	// so a route that slowed down is judged by how it is now.
	hedgeWindow = 200
)

// HedgeConfig sets when a hedge is sent.
type HedgeConfig struct {
	// After is a fixed delay; zero learns the primary's p95 time to first
	// token instead.
	After time.Duration
	// Fallback is the delay used until a p95 has been learned; zero is
	// DefaultHedgeFallback.
	Fallback time.Duration
	// Floor bounds a learned delay from below; zero is DefaultHedgeFloor.
	Floor time.Duration
}

// TTFT keeps a provider's recent times to first token.
type TTFT struct {
	mu      sync.Mutex
	samples []time.Duration
	next    int
}

// Observe records one time to first token.
func (t *TTFT) Observe(d time.Duration) {
	if t == nil || d <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) < hedgeWindow {
		t.samples = append(t.samples, d)
		return
	}
	t.samples[t.next] = d
	t.next = (t.next + 1) % hedgeWindow
}

// P95 is the 95th percentile of the recent samples, and whether there are
// enough of them to mean anything.
func (t *TTFT) P95() (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) < hedgeMinSamples {
		return 0, false
	}
	s := append([]time.Duration(nil), t.samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, len(s)*95/100)], true
}

// HedgeStats counts what hedging did over a provider's life.
type HedgeStats struct {
	// Sent is how many duplicates were sent; Won how many of them
	// answered first. Failover counts the duplicates sent because the
	// primary failed before its first token, not because it was slow.
	Sent, Won, Failover int
}

// hedged is a Provider that races a second route when the first is slow
// to start.
type hedged struct {
	hedgeBase
	second Provider
	cfg    HedgeConfig
	ttft   *TTFT

	mu    *sync.Mutex
	stats *HedgeStats
}

// Hedge returns primary with hedging onto second. A nil second returns
// primary unchanged. The two must serve the same model names: the
// duplicate is the same request, word for word.
func Hedge(primary, second Provider, cfg HedgeConfig) Provider {
	if second == nil || primary == nil {
		return primary
	}
	if cfg.Fallback <= 0 {
		cfg.Fallback = DefaultHedgeFallback
	}
	if cfg.Floor <= 0 {
		cfg.Floor = DefaultHedgeFloor
	}
	return &hedged{hedgeBase: hedgeBase{inner: primary}, second: second, cfg: cfg, ttft: &TTFT{}, mu: &sync.Mutex{}, stats: &HedgeStats{}}
}

// ForTask implements PerTask: each side gets its own sibling, and the
// learned delay and the counts are shared, since they describe the route
// and not the task.
func (h *hedged) ForTask() Provider {
	c := *h
	c.inner, c.second = ForTaskOr(h.inner), ForTaskOr(h.second)
	return &c
}

// HedgeStats reports the counts so far.
func (h *hedged) HedgeStats() HedgeStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.stats
}

func (h *hedged) count(f func(*HedgeStats)) {
	h.mu.Lock()
	f(h.stats)
	h.mu.Unlock()
}

// delay is how long the primary has before the duplicate goes out.
func (h *hedged) delay() time.Duration {
	if h.cfg.After > 0 {
		return h.cfg.After
	}
	if p95, ok := h.ttft.P95(); ok {
		return max(p95, h.cfg.Floor)
	}
	return h.cfg.Fallback
}

// hedgeContent reports whether an event is the reply itself, as opposed
// to news about getting one (a retry countdown).
func hedgeContent(ev Event) bool {
	return ev.Type != EventStatus && ev.Type != EventRetried
}

// hedgeMsg is one thing an attempt reports to the call that raced it.
type hedgeMsg struct {
	from int
	ev   Event
	done bool
	resp *Response
	err  error
}

// Stream implements Provider. Both attempts run on goroutines of their
// own and report through one channel, so every event reaches emit on the
// calling goroutine, as the Provider contract requires. Until one of them
// produces content, only the primary's status lines are shown; from the
// first content on, the winner's events are passed through and the other
// attempt is cancelled.
func (h *hedged) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	msgs := make(chan hedgeMsg, 64)
	var cancels [2]context.CancelFunc
	started := 0
	start := func(i int, p Provider) {
		actx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		started++
		go func() {
			send := func(m hedgeMsg) {
				select {
				case msgs <- m:
				case <-actx.Done():
				}
			}
			resp, err := p.Stream(actx, req, func(ev Event) { send(hedgeMsg{from: i, ev: ev}) })
			// The result goes out even when this attempt was cancelled:
			// the loop below may still be waiting on it, and returns
			// only once the whole call's context is done.
			select {
			case msgs <- hedgeMsg{from: i, done: true, resp: resp, err: err}:
			case <-ctx.Done():
			}
		}()
	}
	began := time.Now()
	start(0, h.inner)
	timer := time.NewTimer(h.delay())
	defer timer.Stop()
	hedge := func(reason string) {
		if started > 1 {
			return
		}
		timer.Stop()
		h.count(func(s *HedgeStats) { s.Sent++ })
		emit(Event{Type: EventStatus, Text: reason + "; racing " + h.second.Name()})
		start(1, h.second)
	}
	winner := -1
	var errs [2]error
	failed := 0
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			if winner < 0 {
				hedge(fmt.Sprintf("no first token after %s", time.Since(began).Round(100*time.Millisecond)))
			}
		case m := <-msgs:
			if winner >= 0 && m.from != winner {
				continue // the loser, finishing its cancellation
			}
			if m.done {
				if winner < 0 && m.err != nil {
					errs[m.from] = m.err
					failed++
					if m.from == 0 && started == 1 {
						// The primary failed before it said anything. If
						// the failure was the route's, the second route
						// is the retry; if it was the request's (a 400),
						// the duplicate would fail the same way.
						if ctx.Err() == nil && Infrastructure(m.err) {
							h.count(func(s *HedgeStats) { s.Failover++ })
							hedge("primary failed: " + RetryReason(m.err))
							continue
						}
						return nil, m.err
					}
					if failed == started {
						// Both failed; the primary's is the error the
						// user configured for and will recognise.
						return nil, errors.Join(errs[0], fmt.Errorf("hedge (%s): %w", h.second.Name(), errs[1]))
					}
					continue
				}
				if winner < 0 {
					// Answered without a single content event: it won.
					h.won(m.from, began, &cancels)
				}
				return m.resp, m.err
			}
			if winner < 0 {
				if !hedgeContent(m.ev) {
					if m.from == 0 {
						emit(m.ev)
					}
					continue
				}
				winner = m.from
				h.won(winner, began, &cancels)
				timer.Stop()
			}
			emit(m.ev)
		}
	}
}

// won settles the race: the other attempt is cancelled, and the primary's
// time to first token is learned. When the duplicate won, the primary's
// is unknown but at least what it had taken so far, and that bound is
// what is recorded; leaving it out would teach the p95 only the fast
// calls and hedge ever earlier.
func (h *hedged) won(i int, began time.Time, cancels *[2]context.CancelFunc) {
	h.ttft.Observe(time.Since(began))
	if c := cancels[1-i]; c != nil {
		c()
	}
	if i == 1 {
		h.count(func(s *HedgeStats) { s.Won++ })
	}
}

// hedgeBase passes the optional provider interfaces through a wrapper, so
// that wrapping a provider for hedging or resume does not hide its model
// list, its effort tier or its embeddings from the code that asks for
// them by type assertion.
type hedgeBase struct {
	inner Provider
}

// Name implements Provider: the wrapper is the provider it wraps.
func (b hedgeBase) Name() string { return b.inner.Name() }

// ListModels implements ModelLister when the wrapped provider does.
func (b hedgeBase) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if l, ok := b.inner.(ModelLister); ok {
		return l.ListModels(ctx)
	}
	return nil, fmt.Errorf("llm: %s cannot list its models", b.inner.Name())
}

// SupportsEffort implements EffortAware for the wrapped provider.
func (b hedgeBase) SupportsEffort() bool { return HonoursEffort(b.inner) }

// EffortOutsideCacheKey implements EffortCacheNeutral for the wrapped
// provider.
func (b hedgeBase) EffortOutsideCacheKey() bool { return EffortChangeKeepsCache(b.inner) }

// Embed implements Embedder when the wrapped provider does.
func (b hedgeBase) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if e, ok := b.inner.(Embedder); ok {
		return e.Embed(ctx, model, texts)
	}
	return nil, fmt.Errorf("llm: %s has no embeddings endpoint", b.inner.Name())
}
