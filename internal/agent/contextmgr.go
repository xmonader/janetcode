package agent

import (
	"sync"
	"sync/atomic"
	"time"
)

// contextManager is the collaborator that owns the request context: what
// goes into the window, how big it is believed to be, and what happens
// when it fills up. Compaction (compact.go), pruning (prune.go), overflow
// recovery (overflow.go), the typed working-memory record (working.go),
// the token estimate (compact.go, tokens.go), the prompt-cache ledger and
// prefix watch (cachestat.go, prefix.go), the history integrity check
// (integrity.go) and the dynamic system-prompt extra (context.go) all read
// and write the state gathered here.
//
// It is embedded in Agent by value: the zero value is the right starting
// state for every field, and the exported ones (CompactAt, ContextWindow,
// SystemExtra, Pins, PathRules, PathSkills) keep their names and their
// place in Agent's API for the callers that set them.
type contextManager struct {
	// CompactAt is the estimated token count that triggers automatic
	// compaction before a model call. Zero means DefaultCompactAt; negative
	// disables it. Read through Settings, written through UpdateSettings.
	CompactAt int
	// ContextWindow is the model's input limit when known; with CompactAt
	// unset, compaction runs at three quarters of it (capped, see
	// CompactThreshold) instead of DefaultCompactAt. Read through
	// Settings, written through UpdateSettings.
	ContextWindow int

	// summaryFailedAt starts the summaryCooldown after a failed summary.
	summaryFailedAt time.Time
	// compactFloorSaid stops the notice about a too-low compact_at from
	// being repeated on every turn of the session.
	compactFloorSaid bool
	// working is the typed record carried across compactions.
	working WorkingMemory
	// historyRewritten says a prune or compaction edited messages since
	// the last model call, so the next cache miss is expected and is not
	// counted as waste. It is separate from integ.shrunkOK, which the
	// integrity check consumes.
	historyRewritten bool

	// Real input size of the last call, and how many messages it covered,
	// so EstimateTokens only has to guess about what came after.
	lastInputTokens int
	lastInputMsgs   int
	// Estimated size of the tool definitions and the registry version it
	// was measured at; see toolTokens.
	toolTokensFor int
	toolTokensAt  uint64
	// estimate is the last EstimateTokens, published for the status bar.
	estimate atomic.Int64

	cache  cacheLedger // prefix paid for twice, and why
	prefix prefixWatch // where the cached prefix stopped matching
	integ  integrity   // the history-shrank-under-us check

	// SystemExtra, when set, is appended to System. It is evaluated once
	// and reused until compaction or RefreshExtra, so the cached prompt
	// prefix stays valid while memory and skills change underneath.
	SystemExtra func() string
	// extraMu guards extraText/extraFrozen: a front-end refreshes them
	// (/remember, /skills) and /btw builds a system prompt off the turn
	// goroutine.
	extraMu     sync.Mutex
	extraText   string
	extraFrozen bool

	// Pins are user notes kept in the system prompt across compaction.
	// Guarded by Agent.settingsMu, with the other live settings.
	Pins []string

	// PathRules, when set, is asked after each tool call which project
	// rules the paths it touched bring in, and returns the text of those
	// not sent yet (see package rules). Each arrives as harness text
	// before the next model call, so instructions can be added to a
	// session without being added to every request of it.
	PathRules func(paths []string) []string
	// PathSkills is the same hook for skill files that name `paths:` in
	// their frontmatter.
	PathSkills func(paths []string) []string
}

// extra returns the frozen SystemExtra text, evaluating it on first use.
func (c *contextManager) extra() string {
	if c.SystemExtra == nil {
		return ""
	}
	c.extraMu.Lock()
	defer c.extraMu.Unlock()
	if !c.extraFrozen {
		c.extraText, c.extraFrozen = c.SystemExtra(), true
	}
	return c.extraText
}

// RefreshExtra re-evaluates SystemExtra on the next call. Compaction does
// this itself, since the history is rewritten anyway; call it after
// editing skills or memory when the model should see the change now, at
// the cost of one cache miss.
func (c *contextManager) RefreshExtra() {
	c.extraMu.Lock()
	c.extraFrozen = false
	c.extraMu.Unlock()
}

// TokenEstimate is the most recent EstimateTokens value. Unlike
// EstimateTokens it reads no message state, so the UI can call it from
// another goroutine while a turn is appending to the history.
func (c *contextManager) TokenEstimate() int { return int(c.estimate.Load()) }

// rewritten records that compaction or a prune edited the history, so the
// next cache miss is expected rather than waste.
func (c *contextManager) rewritten() {
	c.lastInputTokens, c.lastInputMsgs = 0, 0
	c.integ.shrunkOK, c.historyRewritten = true, true
}
