package agent

import (
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// modelRouter is the session's model-routing collaborator: which model a
// given call goes to when it is not simply the session's own. Two
// mechanisms live here — the fallback chain (fallback.go), which moves a
// failed call to another model, and the experimental step router
// (steproute.go), which sends the mechanical part of a turn to a cheaper
// one — because both answer the same question and both need Route.
//
// It is embedded in Agent by value rather than held as a pointer so that
// the zero Agent is still usable and its exported fields keep their names
// for the front-ends that fill them in (see agent_build.go); the Agent
// methods it owns are promoted, so Agent's method set is unchanged.
type modelRouter struct {
	// Fallbacks are models tried in order when a call fails in a way
	// another model could survive: an exhausted quota, or rate limits
	// and server errors that outlived the provider's own retries. At
	// most MaxFallbacks are used; the one that works becomes the
	// session's model.
	Fallbacks []string
	// Route, when set, resolves a model name to the provider that serves
	// it, so a fallback can be on a different backend. Without it a
	// fallback is sent to the provider already in use.
	Route func(model string) (llm.Provider, string, error)
	// Windows, when set, is the context window of any model by name —
	// the config's model table, the way Prices is its rate table. A
	// fallback and a subagent put on another model take that model's
	// window from it (see WindowOf).
	Windows func(model string) int
	// RouteCheap is the model mechanical steps of a turn are sent to
	// instead of Model -- reads, listings, greps and checks that already
	// passed -- while planning, edits and anything that goes wrong stay
	// on Model. Empty (the default) is off: this is experimental, and
	// stays off by default until an eval run justifies turning it on
	// (see docs/manual/evals.md, "Measuring step routing"). Resolved
	// through Route at call time, the same way a Fallback is; when it
	// cannot be routed, routing is off for the session and one notice
	// says so. See steproute.go for the policy.
	RouteCheap string

	// stepRoute is the turn-scoped state the step-routing policy (see
	// steproute.go) reads and updates: whether anything in this turn
	// already means the rest of it must stay on Model, and whether the
	// step just finished was a clean, mechanical batch worth trusting
	// the next call to RouteCheap.
	stepRoute stepRoute
	// routeCheapState caches the one-time resolution of RouteCheap to a
	// provider (see resolveCheap): whether it has been attempted,
	// whether it failed (routing is then off for the rest of the
	// session and the one notice already fired), and, on success, the
	// provider and its resolved model name.
	routeCheapState routeCheapState
	// routeMu guards RouteCheap and routeCheapState: /config route_cheap
	// changes them from a front-end while the turn goroutine routes.
	routeMu sync.Mutex

	// lastCallModel is the model that answered the most recent call --
	// Model on an ordinary step, a fallback's model, or RouteCheap when
	// the step router sent it there -- so the assistant event logged for
	// it can say which (see session.Event.Model). The lastCallSlot,
	// lastCallProvider, lastCallRoute and lastCallWhy beside it are the
	// same call's routing evidence: which slot, which provider, what
	// kind of detour (route/escalate/fallback) and the reason in the
	// policy's own words (see session.Event.Slot and friends).
	lastCallModel    string
	lastCallSlot     string
	lastCallProvider string
	lastCallRoute    string
	lastCallWhy      string
	// lastServed and lastMismatch are the most recent call's provenance
	// and its verdict (see internal/provenance); loggedServed is the last
	// provenance written to the log, so an unchanged one is not repeated
	// on every assistant event.
	lastServed   llm.Provenance
	lastMismatch string
	loggedServed llm.Provenance
	// lastTTFT and lastDecode split the most recent call into waiting
	// for the provider to start and the provider generating. A single
	// duration cannot tell a queued call from a long answer, and they
	// want different responses: one is the provider, the other is the
	// prompt. Both are recorded on the assistant event so a session read
	// back weeks later still has them.
	lastTTFT   time.Duration
	lastDecode time.Duration
}

// SetRouteCheap changes RouteCheap mid-session, for /config route_cheap.
// A new value invalidates whatever resolveCheap cached (a stale
// provider, or a stale "cannot be routed" notice that no longer applies).
func (r *modelRouter) SetRouteCheap(name string) {
	r.routeMu.Lock()
	r.RouteCheap = name
	r.routeCheapState = routeCheapState{}
	r.routeMu.Unlock()
}

// RouteCheapName is RouteCheap, safe to read while a turn runs.
func (r *modelRouter) RouteCheapName() string {
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	return r.RouteCheap
}

// WindowOf is the context window of model: the session's own for the
// session's model, else the Windows lookup the front end wired in, else
// the built-in table (0 when that does not know it either — unknown, not
// the window of some other model).
func (a *Agent) WindowOf(model string) int {
	if s := a.Settings(); model == s.Model && s.ContextWindow > 0 {
		return s.ContextWindow
	}
	if a.Windows != nil {
		if w := a.Windows(model); w > 0 {
			return w
		}
	}
	return llm.KnownWindow(model)
}

// newTurn resets the turn-scoped routing state.
func (r *modelRouter) newTurn() { r.stepRoute = stepRoute{} }
