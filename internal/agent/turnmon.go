package agent

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// turnMonitor is the collaborator that watches a turn happen: what it has
// cost (Usage, turnUse, MaxCostUSD — see budget.go), what it has done
// (stats.go), and whether it is going anywhere — the trajectory predictor
// that scores it (trajectory.go), the loop detector that notices it
// repeating itself (loop.go) and the edit coach that notices the same
// mistake twice (coach.go). None of it decides anything about the
// conversation; it observes the turn and warns.
//
// Embedded in Agent by value, so Usage, MaxCostUSD and Predictor keep
// their names for the callers that read and set them.
//
// usageMu also guards the context manager's cache ledger and prefix
// watch: they are written from the same place the usage is (addUsage and
// callOn), by the turn goroutine, and read from the same place — the
// status bar's goroutine.
type turnMonitor struct {
	stats statsCounter

	// Usage accumulates over the whole conversation.
	Usage llm.Usage
	// turnUse is what the turn in progress has cost so far.
	turnUse llm.Usage
	// Prices, when set, is the rate for any model by name — the config's
	// "prices" and the rate table behind it. Settings.Price covers only
	// the model the session is set to, which is not the only one it pays
	// for. Nil means only that model can be priced.
	Prices func(model string) *llm.Price
	// byModel splits Usage by the model that served each call, so a
	// session that routed, fell back, asked an advisor or delegated can
	// say which model spent what. Guarded by usageMu with the rest.
	byModel map[string]*ModelUsage
	usageMu sync.Mutex // the status bar reads Usage while a turn runs
	// MaxCostUSD, when set, ends a run once Usage has cost this much. It
	// is checked after every model call and after every batch of tool
	// results, not only between turns; see budget.go. Zero means no
	// ceiling, and a model with no price cannot be held to one. Read
	// through MaxCost, written through SetMaxCost.
	MaxCostUSD       float64
	budgetMu         sync.Mutex
	budget           *sharedBudget
	requestSessionID string // usageMu guards the fallback ID for unsaved sessions
	// MaxToolCalls, when set, ends a run before a batch of tool calls
	// that would take its total past this many (see toollimit.go).
	MaxToolCalls int

	// Predictor, when set, scores the running turn from its trajectory
	// features (see Trajectory) and warns the model once per turn when
	// the score is low. Trained from eval runs with Train.
	Predictor *Predictor
	turnStart time.Time
	warned    bool
	lastScore float64
	stuck     bool

	loops   loopDetector
	loopMu  sync.Mutex
	coach   coach
	coachMu sync.Mutex // read_file runs in parallel goroutines
	// outBoost doubles the output limit for the rest of a turn each time a
	// reply in it hit the limit (see maxTokens).
	outBoost int
	// planCuts counts the replies the plan budget ended this turn;
	// planCutNow marks the latest reply as one of them until
	// continueCutReply has answered it.
	planCuts   int
	planCutNow bool
	// burstRan is how many calls of a reply the burst cap cut down were
	// run, until the note saying so goes out with their results.
	burstRan    int
	burstRepeat bool
	// pl is the per-turn state of the loop behaviours ported from
	// hero_agent (planCarry, stepdedup, edgerules, steplimit, risk).
	pl portLoop
	// effort is the effort controller's state for this turn;
	// effortIdleSaid keeps its "on but idle" notice to one a session;
	// lastCallEffort is the tier the controller gave the latest call,
	// for its assistant event (see effortctl.go).
	effort         effortControl
	effortIdleSaid bool
	lastCallEffort string
	// reviewSteps counts the tool steps since the spec review sent the
	// turn back, -1 before it does (see specreview.go).
	reviewSteps int
	// wrap counts the calls made since the turn first passed its checks
	// (see wrapup.go).
	wrap wrapState
	// wrapStop counts the calls after the wrap-up note (wrapstop.go).
	wrapStop wrapStopState
	// postDiag holds the language-server checks of this turn's edits
	// that run beside it (see postdiag.go).
	postDiag postDiag

	advisorUses int32
	// advisorForced counts the consults the harness bought this turn, as
	// opposed to advisorUses, which counts the ones the model asked for.
	advisorForced   int32
	compactPrefired bool
}

// Stats returns a snapshot of the counters.
func (m *turnMonitor) Stats() Stats {
	m.stats.mu.Lock()
	defer m.stats.mu.Unlock()
	s := m.stats.s
	s.Tools = make([]ToolStat, 0, len(m.stats.tools))
	for _, t := range m.stats.tools {
		s.Tools = append(s.Tools, *t)
	}
	sort.Slice(s.Tools, func(i, j int) bool {
		if s.Tools[i].Calls != s.Tools[j].Calls {
			return s.Tools[i].Calls > s.Tools[j].Calls
		}
		return s.Tools[i].Name < s.Tools[j].Name
	})
	s.Models = make([]ModelStat, 0, len(m.stats.models))
	for _, ms := range m.stats.models {
		s.Models = append(s.Models, *ms)
	}
	sort.Slice(s.Models, func(i, j int) bool {
		if s.Models[i].Calls != s.Models[j].Calls {
			return s.Models[i].Calls > s.Models[j].Calls
		}
		return s.Models[i].Name < s.Models[j].Name
	})
	return s
}

// TurnUsage is what the current turn has cost across all of its model
// calls. The last call's usage is not the turn's: a turn that took four
// calls to finish paid for four, and reporting only the last one makes a
// long turn look like a short one.
func (m *turnMonitor) TurnUsage() llm.Usage {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	return m.turnUse
}

// startTurn resets per-turn counters: usage, advisor quota, compact prefire.
func (m *turnMonitor) startTurn() {
	m.usageMu.Lock()
	m.turnUse = llm.Usage{}
	m.usageMu.Unlock()
	atomic.StoreInt32(&m.advisorUses, 0)
	atomic.StoreInt32(&m.advisorForced, 0)
	m.compactPrefired = false
}

// UsageSnapshot is a copy of the usage so far, safe to read while a turn
// is running.
func (m *turnMonitor) UsageSnapshot() llm.Usage {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	return m.Usage
}

// LastScore is the most recent critic score, or NaN before any.
func (m *turnMonitor) LastScore() float64 {
	if m.lastScore == 0 {
		return math.NaN()
	}
	return m.lastScore
}
