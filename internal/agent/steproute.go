package agent

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// Step-level model routing (experimental, off by default — see RouteCheap
// and route_cheap in docs/manual/configuration.md).
//
// Most of what a turn does is mechanical: read a file, list a directory,
// grep, run a check that was already known to pass. None of that needs the
// model the user picked for planning and debugging; it needs a model that
// will not misread a diff. The idea is to send exactly that part of the
// turn to a cheaper model and keep everything that plans, edits or reacts
// to a failure on the model the user chose.
//
// The policy below is deliberately not learned: every rule is one a
// session can name for itself ("that call routed because the last three
// steps were clean reads"), and every refusal to route can say why. The
// trajectory Predictor (see trajectory.go) is a fitted model with its own
// job — flagging a turn that is going badly — and mixing that judgement
// into which model answers a call would make a routing mistake look like
// a training problem instead of a rule to fix. If a future version wants
// the predictor's score as one more input, it can be added as a named
// rule ("route only when the live score is above N") the same way the
// others are, not as a replacement for them.
//
// A step is routed to RouteCheap only when ALL of these hold:
//
//  1. it is not the first model call of the turn — the first call is
//     always the one deciding what the turn even is;
//  2. nothing that means "pay attention" has happened yet this turn: no
//     verify/accept gate failure, no critic warning, no loop nudge, no
//     user steering note. Any of these taints the rest of the turn, not
//     just the next step — a turn that needed correcting once gets the
//     harness's full attention for what follows, not just for the one
//     call right after;
//  3. the previous step was a clean, mechanical batch: every tool call in
//     it succeeded, and every one of them was a read (read_file,
//     list_dir, glob, grep, repo_map) or a shell command isCheck
//     recognises as a check that came back green. A step that ends in
//     plain text (a question, a plan, an answer) is never mechanical by
//     this rule, whatever sent the turn back for another call — the model
//     stopped to think, and the next call should get the model that can.
//
// Any failure, any gate, or the model ending its turn sends the very next
// call back to Model; the taint keeps every later call in the turn there
// too. Routing is also always off in plan mode and while an /accept
// proposal is being written (see session.AcceptWriting), and it only ever
// applies to the turn loop's own calls — compaction, titles, AskOnce and
// every other side call go straight to Model or whatever they already use,
// because they never go through Agent.call.

// mechanicalTools are the read-only tools trusted enough, and predictable
// enough in what they can go wrong, to hand to a smaller model. A bash
// call earns the same trust only when isCheck recognises it and it
// passed (see stepMechanical).
var mechanicalTools = map[string]bool{
	"read_file": true,
	"list_dir":  true,
	"glob":      true,
	"grep":      true,
	"repo_map":  true,
}

// stepMechanical reports whether one tool call is the kind this policy
// trusts a cheap model's continuation to have understood: a plain read, or
// a check command. isErr is the call's own result, since a check that
// failed is a fact worth the main model's attention, not a green light.
func stepMechanical(name string, input []byte, isErr bool) bool {
	if isErr {
		return false
	}
	if mechanicalTools[name] {
		return true
	}
	return name == "bash" && shell.IsCheck(shell.CommandOf(string(input)))
}

// allMechanicalStep reports whether every tool call in one step was
// mechanical and succeeded. uses and results are executeAll's own
// arguments and return value, in the same order; an empty batch is not
// mechanical — there is nothing to trust.
func allMechanicalStep(uses, results []llm.Block) bool {
	if len(uses) == 0 || len(results) != len(uses) {
		return false
	}
	for i, u := range uses {
		// The bash tool reports a non-zero exit in its text, not as an
		// error, so a red check reads as clean unless the exit line is
		// read too (runFailed, the verdict's own test).
		failed := runFailed(session.Event{IsError: results[i].IsError, Output: results[i].Text, Arguments: string(u.Input)})
		if !stepMechanical(u.Name, u.Input, failed) {
			return false
		}
	}
	return true
}

// stepRoute is the turn-scoped state the policy reads and updates. It is
// reset at the start of every turn (see RunBlocks) and never revisits a
// taint once set: nothing un-taints a turn early.
type stepRoute struct {
	tainted  bool
	taintWhy string
	// prevMechanical is whether the step that just finished was a clean,
	// mechanical batch (see allMechanicalStep). Updated after every step,
	// including the ones that ended in text, so a stale true from three
	// steps ago can never leak into a later decision.
	prevMechanical bool
}

// taint marks the rest of the turn as needing the main model's attention.
// The first reason recorded is the one reported; later calls are no-ops,
// since nothing about a second reason changes the decision.
func (r *stepRoute) taint(why string) {
	if !r.tainted {
		r.tainted, r.taintWhy = true, why
	}
}

// routeCheapSmallContext bounds the "remaining context is small" half of
// the cache rule below: at or under this many estimated tokens, paying to
// re-warm the prompt cache from cold — the whole prefix billed as fresh
// input, which is what a model switch costs regardless of provider (see
// cachestat.go) — is cheap in absolute terms even though it is a fresh
// cost, so a cross-provider route is allowed. Past it the same miss is
// real money, and only a same-provider route (which nothing else about
// the request changes) is trusted to be worth it.
const routeCheapSmallContext = 20_000

// routeCheapState caches the one-time resolution of RouteCheap: whether it
// has been attempted, whether it failed (in which case routing is off for
// the rest of the session and the one notice already fired), and, on
// success, the provider and its resolved model name. Whether that
// provider is the one Model runs on is not cached: a fallback or a
// /model switch moves the session to another backend without touching
// route_cheap, and the answer from before the move would then be wrong.
type routeCheapState struct {
	resolved bool
	disabled bool
	prov     llm.Provider
	model    string
}

// sameBackend is Provider.Name() equality: not pointer identity, which a
// per-task provider sibling (see llm.ForTaskOr) would defeat even when the
// two are the very same configured backend.
func sameBackend(a, b llm.Provider) bool {
	return a != nil && b != nil && a.Name() == b.Name()
}

// resolveCheap resolves RouteCheap through Route once per session (or
// once per change: settingsapply.Apply clears this when route_cheap is set live)
// and caches the result. A model that cannot be routed turns the feature
// off for the rest of the session with one notice, exactly as the config
// key's doc promises, rather than retrying — and failing the same way —
// on every step. cur is the provider the session's own model runs on.
func (r *modelRouter) resolveCheap(cur llm.Provider, emit func(Event)) (llm.Provider, string, bool, bool) {
	r.routeMu.Lock()
	name, st := r.RouteCheap, r.routeCheapState
	r.routeMu.Unlock()
	if st.resolved {
		return st.prov, st.model, sameBackend(st.prov, cur), !st.disabled
	}
	// Route runs without the lock; only the turn goroutine resolves, and a
	// /config that changed the name meanwhile wins over this result.
	st = routeCheapState{resolved: true}
	var notice string
	if r.Route == nil {
		st.disabled = true
		notice = "route_cheap is set to " + name + " but no model router is configured; step routing is off for this session"
	} else if prov, model, err := r.Route(name); err != nil {
		st.disabled = true
		notice = "route_cheap: cannot route to " + name + ": " + err.Error() + "; step routing is off for this session"
	} else if prov == nil && cur == nil {
		// Nothing to send the call to: routing with a nil provider would
		// be a panic one step later.
		st.disabled = true
		notice = "route_cheap: " + name + " resolved to no provider; step routing is off for this session"
	} else {
		if prov == nil {
			// A router that answers with no provider means the session's
			// own serves that model (see sideQuery).
			prov = cur
		}
		st.prov, st.model = prov, model
	}
	r.routeMu.Lock()
	if r.RouteCheap == name {
		r.routeCheapState = st
	}
	r.routeMu.Unlock()
	if notice != "" {
		emit(Event{Kind: EvNotice, Text: notice})
		return nil, "", false, false
	}
	return st.prov, st.model, sameBackend(st.prov, cur), true
}

// routeStep decides, for the call about to be made, whether it goes to
// RouteCheap instead of Model. It reports the provider, the resolved
// model name, and the reason in the policy's own words — which branch
// of the policy said yes — plus whether routing applies at all; when it
// does not, the caller sends the call to Model exactly as it would with
// the feature off.
func (a *Agent) routeStep(emit func(Event)) (llm.Provider, string, string, bool) {
	if a.RoutingOff() || a.RouteCheapName() == "" || !a.routingAllowed() {
		return nil, "", "", false
	}
	// The cheap slot filled with the session's own model is the slot
	// resolver's fallback written out by hand (see config.SlotIsMain),
	// and routing a call from a model to itself changes nothing while
	// counting as a route and printing a line about one. So it is not a
	// route: the call goes where it was going anyway.
	if strings.EqualFold(a.RouteCheapName(), a.Settings().Model) {
		return nil, "", "", false
	}
	// The first call of a turn has no step behind it to judge, so it stays
	// on the session's model — unless the intake consumer has classified
	// the session's first prompt as small and read-only, which is a
	// judgement of the same kind made before the turn rather than during
	// it. Nothing else about the policy is relaxed: the taint and the
	// warning still stop the route here, and every later call in the turn
	// still has to follow a mechanical step.
	first := a.stepN <= 1
	if first && !a.intakeRoutedCheap() {
		return nil, "", "", false
	}
	if a.stepRoute.tainted || a.warned {
		return nil, "", "", false
	}
	if !first && !a.stepRoute.prevMechanical {
		return nil, "", "", false
	}
	prov, model, sameProvider, ok := a.modelRouter.resolveCheap(a.Settings().Provider, emit)
	if !ok {
		return nil, "", "", false
	}
	if !sameProvider && a.EstimateTokens() > routeCheapSmallContext {
		return nil, "", "", false
	}
	if first {
		return prov, model, "intake classified the prompt as small and read-only", true
	}
	return prov, model, "the previous step was a clean, mechanical batch", true
}
