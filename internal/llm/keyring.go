package llm

import (
	"strings"
	"sync"
	"time"
)

// KeyRing hands out API keys and remembers which ones are unusable, so a
// key that was rejected or rate limited is not tried again on every request
// until its penalty expires. When every key is tripped the breakers are
// ignored and the one that recovers soonest is used.
type KeyRing struct {
	mu      sync.Mutex
	keys    []string
	idx     int
	until   []time.Time // when key i may be used again; zero when healthy
	strikes []int       // consecutive rate limits, for growing backoff
	now     func() time.Time
}

// Failure classifies why a key could not be used.
type Failure int

const (
	// FailAuth is a rejected key: 401/403. Out for an hour.
	FailAuth Failure = iota
	// FailQuota is exhausted credit: waiting minutes will not refill it.
	FailQuota
	// FailRateLimit is a transient 429/529: short, growing backoff.
	FailRateLimit
)

const (
	authPenalty      = time.Hour
	quotaPenalty     = 30 * time.Minute
	rateLimitPenalty = 15 * time.Second
	maxRatePenalty   = 5 * time.Minute
)

// NewKeyRing dedupes and trims keys. An empty ring holds one empty key so
// keyless local servers work.
func NewKeyRing(keys []string) *KeyRing {
	var out []string
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return &KeyRing{keys: out, until: make([]time.Time, len(out)), strikes: make([]int, len(out)), now: time.Now}
}

// Keys lists the distinct keys in order.
func (r *KeyRing) Keys() []string { return append([]string(nil), r.keys...) }

// Len is the number of distinct keys.
func (r *KeyRing) Len() int { return len(r.keys) }

// Current returns the key to use and its index.
func (r *KeyRing) Current() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idx = r.pick(r.idx)
	return r.keys[r.idx], r.idx
}

// pick returns from, if it is healthy, else the next healthy key, else the
// key whose penalty expires first.
func (r *KeyRing) pick(from int) int {
	now := r.now()
	n := len(r.keys)
	best, bestAt := from, r.until[from]
	for d := 0; d < n; d++ {
		i := (from + d) % n
		if !r.until[i].After(now) {
			return i
		}
		if r.until[i].Before(bestAt) {
			best, bestAt = i, r.until[i]
		}
	}
	return best
}

// Fail records that key i failed with f and moves on. It reports whether a
// different, healthy key is available to retry with right now.
func (r *KeyRing) Fail(i int, f Failure) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.keys) {
		return false
	}
	now := r.now()
	var penalty time.Duration
	switch f {
	case FailAuth:
		penalty = authPenalty
	case FailQuota:
		penalty = quotaPenalty
	default:
		// Shift with a bounded exponent. Past 2^5 the growing backoff
		// exceeds the cap anyway — but shifting by 30 or more overflows
		// the duration to a negative value, the cap check passes, and
		// the key reads as healthy again: the breaker silently dies in
		// exactly the sustained 429 storm it exists for.
		shift := min(r.strikes[i], 5)
		penalty = rateLimitPenalty << shift
		if penalty > maxRatePenalty {
			penalty = maxRatePenalty
		}
		r.strikes[i]++
	}
	r.until[i] = now.Add(penalty)
	next := r.pick((i + 1) % len(r.keys))
	r.idx = next
	return next != i && !r.until[next].After(now)
}

// OK records a success for key i: its rate-limit strikes reset.
func (r *KeyRing) OK(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= 0 && i < len(r.keys) {
		r.strikes[i] = 0
		r.until[i] = time.Time{}
	}
}

// Healthy counts keys with no active penalty.
func (r *KeyRing) Healthy() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	n := 0
	for _, u := range r.until {
		if !u.After(now) {
			n++
		}
	}
	return n
}

// ClassifyFailure maps an HTTP status and message to a Failure.
func ClassifyFailure(status int, msg string) Failure {
	l := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403:
		return FailAuth
	case status == 402 || strings.Contains(l, "quota") || strings.Contains(l, "credit") || strings.Contains(l, "billing") || strings.Contains(l, "insufficient"):
		return FailQuota
	}
	return FailRateLimit
}
