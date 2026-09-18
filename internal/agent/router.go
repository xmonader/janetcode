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
	// provider, its resolved model name, and whether that provider is
	// the one Model already runs on.
	routeCheapState routeCheapState
	// routeMu guards RouteCheap and routeCheapState: /config route_cheap
	// changes them from a front-end while the turn goroutine routes.
	routeMu sync.Mutex

	// lastCallModel is the model that answered the most recent call --
	// Model on an ordinary step, a fallback's model, or RouteCheap when
	// the step router sent it there -- so the assistant event logged for
	// it can say which (see session.Event.Model).
	lastCallModel string
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

// newTurn resets the turn-scoped routing state.
func (r *modelRouter) newTurn() { r.stepRoute = stepRoute{} }
