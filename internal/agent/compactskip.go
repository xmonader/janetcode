package agent

import (
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// Plan first, pay second: before a compaction asks the model for a
// summary, the size of the result is projected from what is known without
// the model (the working-memory record, the files-touched list, and an
// allowance for the summary), and the call is not made when the context
// would shrink by less than compactMinShrink. hero_agent's
// ContextManager::summary_plan does this (#869): a compaction that cannot
// pay for itself used to run anyway, once per step under a low threshold,
// each time a full summary call on top of a window that stayed as large.
// A skipped attempt changes nothing and is not a failed summary, so it
// neither starts the summary cooldown nor counts against anything.
// The overflow retry in callStep is exempt: there the window is already
// too small and a summary is the only way on. The current request is also
// kept verbatim past a compaction (hero_agent #869): the working record
// shortens it to 600 characters, so when the kept tail no longer holds it
// the note carries it whole (compactRequestNote).
// JANETCODE_OFF=compactskip turns both off.

// compactMinShrink is the share of the context a summary compaction must
// remove to be worth its call (hero_agent COMPACTION_MIN_SHRINK).
const compactMinShrink = 0.05

// Allowances in characters, as in hero_agent: the pointer and note
// framing always, the summary text unless the record is already rich.
const (
	pointerAllowanceChars = 200
	summaryAllowanceChars = 2048
)

// compactSkipFloor is the context size below which the projection is not
// trusted: the 2048-character summary allowance is most of a small
// context, and a deliberately low compact threshold (tests, tiny windows)
// would otherwise never summarise. Deviation from hero_agent, which has no
// floor.
const compactSkipFloor = 8_000

// summaryWouldShrink reports whether replacing head by the deterministic
// digest (plus a summary, unless rich) cuts the context by compactMinShrink.
func (a *Agent) summaryWouldShrink(before int, head []llm.Message, rich bool) bool {
	if before < compactSkipFloor {
		return true // the fixed allowances are too coarse to judge a small context
	}
	digest := len(a.working.Render()) + len(touchedNote(head)) + pointerAllowanceChars
	if !rich {
		digest += summaryAllowanceChars
	}
	projected := before - chars(head)/4 + digest/4 + 4
	return float64(projected) <= float64(before)*(1-compactMinShrink)
}

// compactRequestNote is the section that keeps the user's current request
// verbatim when neither the kept tail nor the pending message holds it
// and the working record would have shortened it. "" when nothing needs
// adding.
func (a *Agent) compactRequestNote(tail []llm.Message, pending *llm.Message) string {
	if a.Off["compactskip"] {
		return ""
	}
	req := strings.TrimSpace(a.integ.userText)
	if req == "" || (len(req) <= 600 && !a.Off["working"]) {
		return ""
	}
	held := func(m llm.Message) bool {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && strings.Contains(b.Text, req) {
				return true
			}
		}
		return false
	}
	if pending != nil && held(*pending) {
		return ""
	}
	for _, m := range tail {
		if held(m) {
			return ""
		}
	}
	const maxRequest = 12_000
	if len(req) > maxRequest {
		req = req[:maxRequest] + " [cut]"
	}
	return "The user's current request, verbatim:\n" + req
}
