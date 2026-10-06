package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/llm"
)

// EffortCommand is /effort in every front-end: alone it reports the tier
// the session is asking for and whether the provider behind it does
// anything with one; a tier name sets it; up and down step the ladder.
//
// It changes the session only. The tier rides on every request from the
// next one onward (see Agent.Effort), so nothing has to be rebuilt and
// nothing is written to the config file — /config effort=… is the
// spelling that persists.
func (a *Agent) EffortCommand(arg string) (cmdresult.Result, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	cur := a.Settings()
	switch arg {
	case "":
		return cmdresult.Result{Sheet: a.effortSheet(cur), SheetTitle: "effort", Plain: true}, nil
	case "up", "down", "+", "-":
		delta := 1
		if arg == "down" || arg == "-" {
			delta = -1
		}
		next, moved := llm.StepEffort(cur.Effort, delta)
		if !moved {
			return cmdresult.Result{Notes: []string{"effort is already at " + effortName(cur.Effort)}}, nil
		}
		return a.setEffort(next), nil
	case "default", "off", "auto":
		// The rung below "low": no tier is asked for at all.
		return a.setEffort(""), nil
	}
	if !llm.ValidEffort(arg) {
		return cmdresult.Result{Error: fmt.Sprintf("unknown effort %q · the tiers are %s (or default for the provider's own)", arg, llm.EffortList())}, nil
	}
	return a.setEffort(arg), nil
}

// setEffort writes the tier onto the live settings and says what it did.
func (a *Agent) setEffort(tier string) cmdresult.Result {
	a.UpdateSettings(func(s *Settings) { s.Effort = tier })
	note := "effort " + effortName(tier) + " · from the next model call"
	res := cmdresult.Result{Notes: []string{note}}
	if !a.effortHonoured() {
		res.Tips = []string{"this provider does not take a reasoning tier, so the setting is remembered but not sent; it applies to Anthropic, native Gemini and OpenAI Responses providers"}
	}
	return res
}

// effortName spells a tier for a person, including the empty one.
func effortName(tier string) string {
	if tier == "" {
		return "default (the provider's own)"
	}
	return tier
}

// effortHonoured reports whether the session's provider asks for a tier.
func (a *Agent) effortHonoured() bool {
	prov := a.Settings().Provider
	return prov != nil && llm.HonoursEffort(prov)
}

func (a *Agent) effortSheet(cur Settings) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "effort: %s\n", effortName(cur.Effort))
	sb.WriteString("tiers:  ")
	for i, e := range llm.EffortTiers {
		if i > 0 {
			sb.WriteString("  ")
		}
		if e == cur.Effort {
			sb.WriteString("[" + e + "]")
		} else {
			sb.WriteString(e)
		}
	}
	sb.WriteString("\n")
	switch {
	case cur.Provider == nil:
		sb.WriteString("no provider yet: the tier will apply to whichever one you connect to, if it takes one\n")
	case a.effortHonoured():
		fmt.Fprintf(&sb, "%s asks for the tier on every call; a model that will not take this one is served the tier below it instead\n", cur.Provider.Name())
	default:
		fmt.Fprintf(&sb, "%s does not take a reasoning tier, so this setting is remembered but not sent; it applies to Anthropic, native Gemini and OpenAI Responses providers\n", cur.Provider.Name())
	}
	sb.WriteString("/effort <tier> changes this session · /effort up and /effort down step the ladder · /config effort=<tier> also writes it to the config file")
	return sb.String()
}
