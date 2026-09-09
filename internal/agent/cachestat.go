package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Prompt-cache accounting.
//
// A cache miss is invisible: the turn works, the answer is right, and the
// bill is several times larger. The only signal is that a request which
// should have read its prefix from the cache paid for it again, and that
// only shows up by comparing one model call with the one before it.
//
// So each call is compared with its predecessor: of the prefix the two
// share, how much did this one actually read from cache? What it did not
// read, it paid for. The difference is attributed to a cause — the cache
// expired while the user was reading, the model changed, the history was
// rewritten — because the fix differs for each.

const (
	// cacheNoiseFloor is how many missed tokens are worth reporting.
	// Breakpoint granularity means a few hundred always differ.
	cacheNoiseFloor = 1024
	// cacheTTL is how long a provider keeps a prefix by default. Idle
	// longer than this and the next call cannot hit, whatever it does.
	cacheTTL = 5 * time.Minute
)

// cacheMiss is one model call that paid for a prefix it could have read.
type cacheMiss struct {
	Turn   int
	Tokens int
	Idle   time.Duration
	Cause  string
}

// cacheLedger accumulates misses across a session.
type cacheLedger struct {
	prevPrompt int // input + cache read of the previous call
	prevModel  string
	prevAt     time.Time
	reported   bool // the provider has reported a cache read at least once
	misses     []cacheMiss
	total      int
	calls      int
}

// record folds in one model call. rewritten says the history was edited
// since the last call (a compaction or a prune), which legitimately
// invalidates the prefix and is not counted as waste.
func (l *cacheLedger) record(u llm.Usage, model string, turn int, at time.Time, rewritten bool) {
	prompt := u.InputTokens + u.CacheReadTokens
	if u.CacheReadTokens > 0 {
		l.reported = true
	}
	defer func() {
		l.prevPrompt, l.prevModel, l.prevAt = prompt, model, at
		l.calls++
	}()
	if l.calls == 0 || rewritten {
		return
	}
	// Of the prefix these two calls share, what was not read from cache
	// was paid for twice.
	shared := min(l.prevPrompt, prompt)
	missed := shared - u.CacheReadTokens
	if missed < cacheNoiseFloor {
		return
	}
	idle := at.Sub(l.prevAt)
	cause := "the prefix changed"
	switch {
	case !l.reported:
		cause = "this endpoint reports no cache reads at all"
	case model != l.prevModel:
		cause = "the model changed, so the whole prompt is billed again"
	case idle > cacheTTL:
		cause = fmt.Sprintf("idle %s, past the ~%s the cache is kept", idle.Round(time.Second), cacheTTL)
	}
	l.misses = append(l.misses, cacheMiss{Turn: turn, Tokens: missed, Idle: idle, Cause: cause})
	l.total += missed
}

// CacheReport renders what the session paid twice for.
func (a *Agent) CacheReport() string {
	a.usageMu.Lock()
	l := a.cache
	used := a.Usage
	a.usageMu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "%d model call(s) · %s read from cache · %s billed as fresh input\n",
		l.calls, shortInt(used.CacheReadTokens), shortInt(used.InputTokens))
	if l.calls < 2 {
		b.WriteString("\nnot enough calls yet to say whether the cache is working")
		return b.String()
	}
	if len(l.misses) == 0 {
		b.WriteString("\nno call paid for a prefix it could have read: the cache is working")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s paid twice across %s:\n", shortInt(l.total), plural(len(l.misses), "call"))
	byCause := map[string]int{}
	for _, m := range l.misses {
		byCause[m.Cause] += m.Tokens
	}
	causes := make([]string, 0, len(byCause))
	for c := range byCause {
		causes = append(causes, c)
	}
	sort.Slice(causes, func(i, j int) bool { return byCause[causes[i]] > byCause[causes[j]] })
	for _, c := range causes {
		fmt.Fprintf(&b, "  %-9s %s\n", shortInt(byCause[c]), c)
	}
	if p := a.Price; p != nil {
		if usd, ok := (llm.Usage{InputTokens: l.total}).Cost(p); ok {
			fmt.Fprintf(&b, "\nabout %s at this model's input price, had none of it been cached\n", llm.FormatUSD(usd))
		}
	}
	b.WriteString("\nworst calls:\n")
	worst := append([]cacheMiss(nil), l.misses...)
	sort.Slice(worst, func(i, j int) bool { return worst[i].Tokens > worst[j].Tokens })
	for i, m := range worst {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "  turn %-3d %-9s %s\n", m.Turn, shortInt(m.Tokens), m.Cause)
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortInt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
