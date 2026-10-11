package agent

import "strings"

// The feature knobs a front-end can change in the middle of a session:
// the cascade's round count, the forced advisor, the stale-check nudge,
// the effort controller,
// the advisor slot and the decision provider with its consumers.
//
// Each of them is a plain field on Agent, assigned once while the agent
// is built and nothing is running. Once a turn is going, the turn
// goroutine reads them and /features, /jev and /config write them from
// the front-end's, so every later change goes through the setters here
// and every read on the turn goroutine through the getters. It is the
// contract RouteCheap already has (see modelRouter.SetRouteCheap) —
// public field, guarded change — rather than a second one.

// SetEscalateAfter changes how many failed verification rounds a turn may
// spend on the session's model before the rest of it goes to the strong
// slot. Zero turns the cascade off.
func (a *Agent) SetEscalateAfter(n int) {
	a.featMu.Lock()
	a.EscalateAfter = n
	a.featMu.Unlock()
}

// EscalateRounds is EscalateAfter, safe to read while a turn runs.
func (a *Agent) EscalateRounds() int {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.EscalateAfter
}

// SetStaleCheckNudge turns the one-per-turn stale-check nudge on or off.
func (a *Agent) SetStaleCheckNudge(on bool) {
	a.featMu.Lock()
	a.StaleCheckNudge = on
	a.featMu.Unlock()
}

// StaleNudgeOn is StaleCheckNudge, safe to read while a turn runs.
func (a *Agent) StaleNudgeOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.StaleCheckNudge
}

// SetEffortControl turns the effort controller on or off. A turn already
// running keeps the tier it has until its next call.
func (a *Agent) SetEffortControl(on bool) {
	a.featMu.Lock()
	a.EffortControl = on
	a.featMu.Unlock()
}

// EffortControlOn is EffortControl, safe to read while a turn runs.
func (a *Agent) EffortControlOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.EffortControl
}

// SetLocalizeFailures turns failure localization on or off for the
// red runs that follow.
func (a *Agent) SetLocalizeFailures(on bool) {
	a.featMu.Lock()
	a.LocalizeFailures = on
	a.featMu.Unlock()
}

// LocalizeOn is LocalizeFailures, safe to read while a turn runs.
func (a *Agent) LocalizeOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.LocalizeFailures
}

// SetSubagentMerge turns the merge-back of editing subagents on or off.
func (a *Agent) SetSubagentMerge(on bool) {
	a.featMu.Lock()
	a.SubagentMerge = on
	a.featMu.Unlock()
}

// SubagentMergeOn is SubagentMerge, safe to read while a turn runs.
func (a *Agent) SubagentMergeOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.SubagentMerge
}

// SetAdvisorMode changes the forced-consult mode ("off" or "always") and
// the per-turn cap; a cap of zero keeps the default.
func (a *Agent) SetAdvisorMode(mode string, maxConsults int) {
	a.featMu.Lock()
	a.AdvisorMode, a.AdvisorMaxConsults = mode, maxConsults
	a.featMu.Unlock()
}

// advisorForcedSettings is the mode and cap, read under the lock.
func (a *Agent) advisorForcedSettings() (string, int) {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return strings.TrimSpace(a.AdvisorMode), a.AdvisorMaxConsults
}

// SetAdvisorModel changes the model the advisor consults. Empty is the
// session's own model, which is also what an unset slot resolves to.
func (a *Agent) SetAdvisorModel(name string) {
	a.featMu.Lock()
	a.AdvisorModel = name
	a.featMu.Unlock()
}

// AdvisorModelName is AdvisorModel, safe to read while a turn runs.
func (a *Agent) AdvisorModelName() string {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return strings.TrimSpace(a.AdvisorModel)
}

// DecisionConsumer names one user of the decision provider. They are the
// names /jev takes, and every one of them is off until it is asked for.
type DecisionConsumer string

const (
	DecisionTriage  DecisionConsumer = "triage"
	DecisionIntake  DecisionConsumer = "intake"
	DecisionRoute   DecisionConsumer = "route"
	DecisionOutcome DecisionConsumer = "outcome"
)

// SetDecisions swaps the decision provider and its consumer flags. A nil
// provider is the feature switched off: every consumer then asks nothing,
// which is exactly what it does when the block was never configured.
func (a *Agent) SetDecisions(p DecisionProvider, triage, intake, intakeRoute, outcome bool) {
	a.featMu.Lock()
	a.Decisions = p
	a.DecisionsTriage, a.DecisionsIntake = triage, intake
	a.DecisionsIntakeRoute, a.DecisionsSessionOutcome = intakeRoute, outcome
	a.featMu.Unlock()
}

// DecisionsOn reports whether a consumer is switched on: its own flag,
// and the kill switch not having taken the whole feature away. It says
// nothing about a client being configured, because the one consumer that
// acts on an answer already in hand (routing) must not change its mind
// halfway through a turn because the client was swapped out under it.
func (a *Agent) DecisionsOn(c DecisionConsumer) bool {
	if a.Off["decisions"] {
		return false
	}
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	switch c {
	case DecisionTriage:
		return a.DecisionsTriage
	case DecisionIntake:
		return a.DecisionsIntake
	case DecisionRoute:
		return a.DecisionsIntakeRoute
	case DecisionOutcome:
		return a.DecisionsSessionOutcome
	}
	return false
}

// decisionsFor returns the provider a consumer should ask, or nil when it
// must not ask: the consumer switched off, the feature killed, or no
// client configured. Every consumer takes the provider from here once and
// uses that value for the rest of the call, so a swap mid-turn cannot
// leave one call half on the old client.
func (a *Agent) decisionsFor(c DecisionConsumer) DecisionProvider {
	if !a.DecisionsOn(c) {
		return nil
	}
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.Decisions
}

// DecisionsClient reports whether a client is configured at all, which is
// what separates "switched off" from "nothing to ask" in a status line.
func (a *Agent) DecisionsClient() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.Decisions != nil
}
