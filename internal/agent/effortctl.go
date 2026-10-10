package agent

import (
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// An effort controller moves the reasoning tier a turn's calls ask for
// with what the turn shows, instead of asking every call for the one tier
// the session was set to. The plan budget, the burst cap and the doubling
// output limit are reflexes that end a reply once it has gone wrong; this
// steers the next call before it does. It is the TCP congestion rule
// applied to thought: each turn starts at the floor ("low"), where the
// first reply has the least room to design the whole change in its
// reasoning (effbench's MiMo spent 23–33k reasoning tokens there); a step
// that went wrong — a failed edit, a red check, a loop note, a verify
// gate sending the turn back — raises the tier one rung (additive
// increase); a step that made progress — a green check, the turn's first
// successful edit to a file — halves the distance above the floor
// (multiplicative decrease); and a reply the plan budget or the output
// limit cut drops it straight back to the floor.
//
// Every signal is one the harness already has; nothing asks the model
// anything. The ceiling is the tier the session was set to (/effort),
// or "high" when that is the provider's default, so the controller only
// ever spends less than the person asked for or the same.
//
// It is off unless effort_control is set, and with it set it still does
// nothing on a provider that sends no tier (the OpenAI-compatible one,
// MiMo's) or whose prompt cache keys on the tier (Anthropic: a change of
// effort invalidates the cached messages). Even where a change is free of
// cache cost, a turn moves the tier at most maxEffortMoves times: the
// controller is meant to set a level, not to dither around one.

// maxEffortMoves bounds how often one turn changes the tier. The reset
// to the floor at turn start is not counted.
const maxEffortMoves = 4

// effortFloorTier is where every turn starts and where progress returns.
const effortFloorTier = "low"

// effortDefaultCeil is the ceiling when the session asks for no tier.
const effortDefaultCeil = "high"

// effortSignal is what one step, or one reply, says about the next call.
type effortSignal int

const (
	// effortSteady leaves the tier where it is.
	effortSteady effortSignal = iota
	// effortProgress halves the distance above the floor.
	effortProgress
	// effortTrouble raises the tier one rung.
	effortTrouble
	// effortOverthink returns to the floor.
	effortOverthink
)

func (s effortSignal) String() string {
	switch s {
	case effortProgress:
		return "progress"
	case effortTrouble:
		return "trouble"
	case effortOverthink:
		return "overthink"
	}
	return "steady"
}

// effortControl is the controller's per-turn state. Ranks are positions
// on llm.EffortTiers.
type effortControl struct {
	rank, ceil int
	moves      int
	// edited is every path a successful edit touched this turn, so only
	// the first edit to each file counts as progress.
	edited map[string]bool
}

// effortCeilRank is the ceiling for a session set to tier.
func effortCeilRank(tier string) int {
	floor := llm.EffortRank(effortFloorTier)
	if r := llm.EffortRank(tier); r >= floor {
		return r
	}
	return llm.EffortRank(effortDefaultCeil)
}

// start resets the controller for a new turn of a session set to tier.
func (c *effortControl) start(tier string) {
	*c = effortControl{rank: llm.EffortRank(effortFloorTier), ceil: effortCeilRank(tier), edited: map[string]bool{}}
}

// setCeil follows a /effort made while the turn runs: the ceiling is
// always what the session is set to now, and the tier never above it.
func (c *effortControl) setCeil(tier string) {
	c.ceil = effortCeilRank(tier)
	c.rank = min(c.rank, c.ceil)
}

// observe applies one signal and reports whether the tier moved.
func (c *effortControl) observe(sig effortSignal) bool {
	if c.moves >= maxEffortMoves {
		return false
	}
	floor := llm.EffortRank(effortFloorTier)
	next := c.rank
	switch sig {
	case effortTrouble:
		next = min(c.rank+1, c.ceil)
	case effortProgress:
		next = floor + (c.rank-floor)/2
	case effortOverthink:
		next = floor
	}
	if next == c.rank {
		return false
	}
	c.rank = next
	c.moves++
	return true
}

// tier is the tier the next call asks for.
func (c *effortControl) tier() string {
	if c.rank < 0 || c.rank >= len(llm.EffortTiers) {
		return ""
	}
	return llm.EffortTiers[c.rank]
}

// loopNoteRe matches the notes the loop detector, the repeated-message
// check and the burst cap's repeat cut append to a tool result
// (loop.go, burst.go): the harness saying the model is going round.
var loopNoteRe = regexp.MustCompile(`\[harness\] (You have gone round the same loop|This exact \S+ call has now failed|This is the \S+ identical|You have gone back and forth|You have read .+ times in the last|You have written this same message|Your last reply started repeating calls)`)

// harnessRefused reports whether a tool result is the harness declining
// to run the call — a permission rule, the user, the test guard — rather
// than the call failing. A refused edit says nothing about the model's
// reasoning.
func harnessRefused(text string) bool {
	for _, p := range refusalPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return strings.Contains(text, " is a test file. This run leaves the repository's tests unchanged")
}

// stepEffortSignal reads one finished tool step for the controller. uses
// and results are the step's calls and their results in the same order,
// with the harness's notes already appended; edited is the turn's set of
// files edited so far, which the step's successful edits join. Trouble
// outranks progress: a step that edited a new file and then ran a red
// check is a step that went wrong. It is a pure function of what a
// session log records, so the live controller and an offline replay
// cannot disagree about a step.
func stepEffortSignal(uses, results []llm.Block, edited map[string]bool) effortSignal {
	trouble, progress := false, false
	for i, u := range uses {
		if i >= len(results) {
			break
		}
		r := results[i]
		if loopNoteRe.MatchString(r.Text) {
			trouble = true
		}
		if harnessRefused(r.Text) {
			continue
		}
		switch {
		case editTools[u.Name]:
			if r.IsError {
				trouble = true
				continue
			}
			if p := inputPath(u.Input); p != "" && !edited[p] {
				edited[p] = true
				progress = true
			}
		case u.Name == "bash" && shell.IsCheck(shell.CommandOf(string(u.Input))):
			ev := session.Event{IsError: r.IsError, Output: r.Text, Arguments: string(u.Input)}
			switch {
			case neverRan(ev):
				// A typo in the command, not a verdict on the code.
			case runFailed(ev):
				trouble = true
			default:
				progress = true
			}
		}
	}
	switch {
	case trouble:
		return effortTrouble
	case progress:
		return effortProgress
	}
	return effortSteady
}

// replyEffortSignal reads a reply before its tools run: one the plan
// budget ended, or one the output limit cut before it called anything,
// thought too long, and the next call starts from the floor.
func replyEffortSignal(resp *llm.Response, planCut bool) effortSignal {
	if planCut || (resp != nil && resp.StopReason == llm.StopMaxTokens && len(resp.Message.ToolUses()) == 0) {
		return effortOverthink
	}
	return effortSteady
}

// effortControlled reports whether the controller sets the tier of a
// call on prov, and says once a session why not when it is on but
// cannot.
func (a *Agent) effortControlled(prov llm.Provider, emit func(Event)) bool {
	if !a.EffortControlOn() || prov == nil {
		return false
	}
	why := ""
	switch {
	case !llm.HonoursEffort(prov):
		why = prov.Name() + " does not send a reasoning tier"
	case !llm.EffortChangeKeepsCache(prov):
		why = prov.Name() + " keys its prompt cache on the tier, so moving it would re-bill the history"
	}
	if why == "" {
		return true
	}
	if !a.effortIdleSaid {
		a.effortIdleSaid = true
		emit(Event{Kind: EvNotice, Text: "effort_control is on but idle: " + why + "; every call asks for the session's tier"})
	}
	return false
}

// callEffort is the tier the next call on prov asks for: the
// controller's when it applies, the session's otherwise. steered says
// which.
func (a *Agent) callEffort(prov llm.Provider, emit func(Event)) (tier string, steered bool) {
	set := a.Settings().Effort
	if !a.effortControlled(prov, emit) {
		return set, false
	}
	a.effort.setCeil(set)
	return a.effort.tier(), true
}

// noteEffort feeds the controller one signal from the turn loop.
func (a *Agent) noteEffort(sig effortSignal) {
	if !a.EffortControlOn() || sig == effortSteady {
		return
	}
	from := a.effort.tier()
	if a.effort.observe(sig) {
		a.debugf("effort %s → %s (%s, move %d of %d)", from, a.effort.tier(), sig, a.effort.moves, maxEffortMoves)
	}
}
