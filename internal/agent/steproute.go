package agent

import (
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
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
	return name == "bash" && isCheck(commandOf(string(input)))
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
		if !stepMechanical(u.Name, u.Input, results[i].IsError) {
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
// success, the provider, its resolved model name, and whether that
// provider is the one Model already runs on.
type routeCheapState struct {
	resolved bool
	disabled bool
	prov     llm.Provider
	model    string
	// sameProvider is Provider.Name() equality with a.Provider: not
	// pointer identity, which a per-task provider sibling (see
	// llm.ForTaskOr) would defeat even when the two are the very same
	// configured backend.
	sameProvider bool
}

// resolveRouteCheap resolves RouteCheap through Route once per session (or
// once per change: ApplyConfig clears this when route_cheap is set live)
// and caches the result. A model that cannot be routed turns the feature
// off for the rest of the session with one notice, exactly as the config
// key's doc promises, rather than retrying — and failing the same way —
// on every step.
func (a *Agent) resolveRouteCheap(emit func(Event)) (llm.Provider, string, bool, bool) {
	a.routeMu.Lock()
	name, st := a.RouteCheap, a.routeCheapState
	a.routeMu.Unlock()
	if st.resolved {
		return st.prov, st.model, st.sameProvider, !st.disabled
	}
	// Route runs without the lock; only the turn goroutine resolves, and a
	// /config that changed the name meanwhile wins over this result.
	st = routeCheapState{resolved: true}
	var notice string
	if a.Route == nil {
		st.disabled = true
		notice = "route_cheap is set to " + name + " but no model router is configured; step routing is off for this session"
	} else if prov, model, err := a.Route(name); err != nil {
		st.disabled = true
		notice = "route_cheap: cannot route to " + name + ": " + err.Error() + "; step routing is off for this session"
	} else {
		same := a.Provider != nil && prov != nil && prov.Name() == a.Provider.Name()
		st.prov, st.model, st.sameProvider = prov, model, same
	}
	a.routeMu.Lock()
	if a.RouteCheap == name {
		a.routeCheapState = st
	}
	a.routeMu.Unlock()
	if notice != "" {
		emit(Event{Kind: EvNotice, Text: notice})
		return nil, "", false, false
	}
	return st.prov, st.model, st.sameProvider, true
}

// SetRouteCheap changes RouteCheap mid-session, for /config route_cheap.
// A new value invalidates whatever resolveRouteCheap cached (a stale
// provider, or a stale "cannot be routed" notice that no longer applies).
func (a *Agent) SetRouteCheap(name string) {
	a.routeMu.Lock()
	a.RouteCheap = name
	a.routeCheapState = routeCheapState{}
	a.routeMu.Unlock()
}

// RouteCheapName is RouteCheap, safe to read while a turn runs.
func (a *Agent) RouteCheapName() string {
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	return a.RouteCheap
}

// routeStep decides, for the call about to be made, whether it goes to
// RouteCheap instead of Model. It reports the provider and resolved model
// name to use and whether routing applies at all; when it does not, the
// caller sends the call to Model exactly as it would with the feature off.
func (a *Agent) routeStep(emit func(Event)) (llm.Provider, string, bool) {
	if a.RouteCheapName() == "" || a.PlanMode {
		return nil, "", false
	}
	if phase, on := a.acceptPhase(); on && phase == session.AcceptWriting {
		return nil, "", false
	}
	if a.stepN <= 1 {
		return nil, "", false
	}
	if (a.stepRoute.tainted || a.warned) || !a.stepRoute.prevMechanical {
		return nil, "", false
	}
	prov, model, sameProvider, ok := a.resolveRouteCheap(emit)
	if !ok {
		return nil, "", false
	}
	if !sameProvider && a.EstimateTokens() > routeCheapSmallContext {
		return nil, "", false
	}
	return prov, model, true
}
