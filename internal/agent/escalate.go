package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
)

// Escalation is the cascade: let the session's own model try, and when the
// real check says it failed, spend the rest of the turn on a stronger one.
//
// The trigger is deliberately not a judgement about the request. A model
// asked whether a task "sounds hard" routes on the wording, pays for the
// strong model on work the cheap one could do, and still misses the one
// task that needed it. The verification gate failing is not an opinion, so
// that is what moves the tier.
//
// Escalation goes UP a tier, so the cross-provider context ceiling that
// guards cheap step routing (routeCheapSmallContext) deliberately does not
// apply here. That ceiling exists because a cold prompt cache is not worth
// paying for a cheaper answer; when the alternative is a turn that fails
// its own gate, it is.

// SetStrongModel changes the strong slot live and drops the cached
// resolution, so a name that was unroutable is tried again under the new
// one rather than staying off for the rest of the session.
func (a *Agent) SetStrongModel(name string) {
	a.escalateState.mu.Lock()
	a.StrongModel = name
	a.escalateState.resolved, a.escalateState.disabled = false, false
	a.escalateState.prov, a.escalateState.model, a.escalateState.onMain = nil, "", false
	a.escalateState.mu.Unlock()
}

// StrongModelName is StrongModel, safe to read while a turn runs: it is
// read under the lock SetStrongModel takes, so /features and /model
// changing the slot cannot race the turn goroutine asking for it.
func (a *Agent) StrongModelName() string {
	a.escalateState.mu.Lock()
	defer a.escalateState.mu.Unlock()
	return strings.TrimSpace(a.StrongModel)
}

// escalateState caches the one-time resolution of StrongModel, the same
// way routeCheapState caches RouteCheap: a name that cannot be routed
// turns escalation off for the session with one notice rather than
// failing identically on every call.
type escalateState struct {
	mu       sync.Mutex
	on       bool // the gate has failed enough rounds; calls go to the strong slot
	why      string
	resolved bool
	disabled bool
	prov     llm.Provider
	model    string
	onMain   bool // prov is the session's own provider, which /connect can swap
}

// EscalateEnabled reports whether the cascade can fire at all: a strong
// slot is configured, a round count is set, and the behaviour is not
// switched off. A strong slot naming the session's own model is the
// slot resolver's fallback written out by hand (see config.SlotIsMain):
// escalating to the same model changes nothing while counting as a
// route, so it is reported as no route instead — the same rule the step
// router applies to route_cheap.
func (a *Agent) EscalateEnabled() bool {
	return !a.Off["escalate"] && a.StrongModelName() != "" &&
		!strings.EqualFold(a.StrongModelName(), a.Settings().Model) &&
		a.EscalateRounds() > 0
}

// escalate turns the rest of the turn over to the strong slot. Repeated
// calls are no-ops, so the reason reported is the failure that first
// earned it.
func (a *Agent) escalate(why string, emit func(Event)) {
	if !a.EscalateEnabled() {
		return
	}
	a.escalateState.mu.Lock()
	first := !a.escalateState.on
	if first {
		a.escalateState.on, a.escalateState.why = true, why
	}
	a.escalateState.mu.Unlock()
	if first && emit != nil {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("escalating to %s: %s", a.StrongModelName(), why)})
	}
}

// escalated reports whether the cascade has fired, and why.
func (a *Agent) escalated() (string, bool) {
	a.escalateState.mu.Lock()
	defer a.escalateState.mu.Unlock()
	return a.escalateState.why, a.escalateState.on
}

// endTurnEscalation clears the cascade at the end of a turn: the next
// request starts on the cheap tier again, which is the whole point of a
// cascade. The resolution of the strong model is kept.
func (a *Agent) endTurnEscalation() {
	a.escalateState.mu.Lock()
	a.escalateState.on, a.escalateState.why = false, ""
	a.escalateState.mu.Unlock()
}

// resolveStrong resolves StrongModel through Route once per session. Like
// resolveCheap, a name that cannot be routed disables the feature for the
// session with a single notice.
func (a *Agent) resolveStrong(emit func(Event)) (llm.Provider, string, bool) {
	s := a.Settings()
	name := a.StrongModelName()
	a.escalateState.mu.Lock()
	resolved, disabled, prov, model, onMain := a.escalateState.resolved, a.escalateState.disabled, a.escalateState.prov, a.escalateState.model, a.escalateState.onMain
	a.escalateState.mu.Unlock()
	if resolved {
		if onMain {
			// The slot rides on the session's provider: follow it when
			// it was swapped, not the one that was live at first use.
			prov = s.Provider
		}
		return prov, model, !disabled
	}
	prov, model, disabled, onMain = s.Provider, name, false, true
	var notice string
	if a.Route != nil {
		p, r, err := a.Route(name)
		switch {
		case err != nil:
			disabled = true
			notice = "strong_model: cannot route to " + name + ": " + err.Error() + "; escalation is off for this session"
		default:
			if p != nil {
				prov, onMain = p, false
			}
			if r != "" {
				model = r
			}
		}
	}
	if !disabled && prov == nil {
		disabled = true
		notice = "strong_model: " + name + " resolved to no provider; escalation is off for this session"
	}
	a.escalateState.mu.Lock()
	if strings.TrimSpace(a.StrongModel) != name {
		// Changed while this resolved (Route runs without the lock):
		// caching the old name's answer would serve the new slot with the
		// old model for the rest of the session. This call stays on the
		// session's model; the next one resolves the new name.
		a.escalateState.mu.Unlock()
		return nil, "", false
	}
	a.escalateState.resolved, a.escalateState.disabled = true, disabled
	a.escalateState.prov, a.escalateState.model, a.escalateState.onMain = prov, model, onMain
	a.escalateState.mu.Unlock()
	if notice != "" {
		if emit != nil {
			emit(Event{Kind: EvNotice, Text: notice})
		}
		return nil, "", false
	}
	return prov, model, true
}

// escalateNearCapAt is the share of a turn's iteration budget after which
// a turn that is still going is treated as not converging.
//
// This is the second trigger, and it exists because of what the first one
// could not see. On the hard eval set the model runs the grading tests
// itself, so by the time it ends a turn the verification gate agrees with
// it and never fails -- the gate confirms rather than catches. The only
// failure in nine base-model runs was a turn that ran out of clock at 35
// model calls, which no gate-failure trigger can reach.
//
// Burning most of the budget is a fact about this run, not a prediction
// about the request, so it keeps the property the cascade is built on.
const escalateNearCapAt = 0.6

// escalateNearCap fires the cascade when a turn has spent most of its
// iteration budget and is still going.
func (a *Agent) escalateNearCap(iter, maxIter int, emit func(Event)) {
	if !a.EscalateEnabled() || maxIter <= 0 {
		return
	}
	if float64(iter) < escalateNearCapAt*float64(maxIter) {
		return
	}
	a.escalate(fmt.Sprintf("the turn has used %d of its %d model calls and is still going", iter, maxIter), emit)
}

// escalateStep reports the provider and model this call should use when
// the cascade has fired, and false when it has not. It is asked before
// the cheap step router, because a turn that is already failing its own
// checks has no business dipping to an even cheaper tier.
func (a *Agent) escalateStep(emit func(Event)) (llm.Provider, string, bool) {
	if !a.EscalateEnabled() {
		return nil, "", false
	}
	if _, on := a.escalated(); !on {
		return nil, "", false
	}
	return a.resolveStrong(emit)
}
