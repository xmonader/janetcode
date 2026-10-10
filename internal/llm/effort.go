package llm

// EffortTiers are the reasoning tiers a request may ask for, weakest
// first. Rank order is what makes them useful beyond a set of names: a
// model that will not take "max" will usually take the tier below it,
// and a person raising or lowering the tier means one step along this
// ladder. Empty — no tier at all — sits below "low" and means whatever
// the provider does by default.
//
// It lives here, not in a provider, because the config file, the agent's
// live settings, the /effort command and every front-end's status line
// all name the same ladder; a provider that cannot ask for a tier simply
// ignores Request.Effort.
var EffortTiers = []string{"low", "medium", "high", "xhigh", "max"}

// EffortRank is the position of tier on the ladder, or -1 when it is not
// a tier (including the empty string, which is the provider default).
func EffortRank(tier string) int {
	for i, e := range EffortTiers {
		if e == tier {
			return i
		}
	}
	return -1
}

// ValidEffort reports whether tier is a tier, or empty for the default.
func ValidEffort(tier string) bool {
	return tier == "" || EffortRank(tier) >= 0
}

// EffortList is the ladder as a readable list, for help and errors.
func EffortList() string {
	out := ""
	for i, e := range EffortTiers {
		if i > 0 {
			out += ", "
		}
		out += e
	}
	return out
}

// StepEffort moves tier by delta rungs and reports whether it moved.
// The empty string is the rung below "low", so stepping up from the
// provider default reaches "low" and stepping down from "low" returns to
// it; past either end nothing moves, which is what lets a caller say
// "already at max" instead of pretending.
func StepEffort(tier string, delta int) (string, bool) {
	rank := EffortRank(tier) // -1 for the default rung
	next := rank + delta
	switch {
	case next < -1, next >= len(EffortTiers):
		return tier, false
	case next == -1:
		return "", tier != ""
	default:
		return EffortTiers[next], EffortTiers[next] != tier
	}
}

// EffortAware is implemented by a provider that actually asks for
// Request.Effort. A front-end reports what a session's effort tier will
// do by asking the provider rather than by knowing which one it is; a
// provider whose request shape has no tier simply does not implement it.
type EffortAware interface {
	SupportsEffort() bool
}

// HonoursEffort reports whether prov asks for a reasoning tier.
func HonoursEffort(prov Provider) bool {
	ea, ok := prov.(EffortAware)
	return ok && ea.SupportsEffort()
}

// EffortCacheNeutral is implemented by a provider whose prompt cache does
// not key on the reasoning tier: a call asking for another tier than the
// one before it still reads the prefix that call wrote. Anthropic does
// not qualify — a change of thinking or effort settings invalidates the
// cached messages — so a harness that moves the tier by itself (the
// agent's effort controller) asks this first and leaves a provider that
// does not say so alone.
type EffortCacheNeutral interface {
	EffortOutsideCacheKey() bool
}

// EffortChangeKeepsCache reports whether a change of Request.Effort
// between two calls on prov keeps its prompt cache. A provider that does
// not say is taken to key on it: a guess the other way re-bills the whole
// history at every change.
func EffortChangeKeepsCache(prov Provider) bool {
	en, ok := prov.(EffortCacheNeutral)
	return ok && en.EffortOutsideCacheKey()
}
