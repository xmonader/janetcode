package agent

import "strings"

// Every prompt says which layer asked and how to stop that layer asking.
// The layers are many (the mode, the tool's own guard, protected paths,
// the egress policy, taint, hooks, ask rules), and a user who is asked
// the same thing again needs to know which switch to reach for. Why and
// Stop are set where the layer decides (setAsk); a call that nothing but
// the mode asked about gets them from modeAsk just before it is put.

// Stop hints, one per layer. Each states only what the code does.
const (
	stopHook      = "edit the hook (the hooks key in the config, or .janetcode/hooks.json); no mode skips a hook that asks"
	stopRule      = "remove or narrow that ask rule under \"permissions\" in the config file or in .janetcode/permissions.json (a managed policy's rule cannot be removed); no mode skips it"
	stopTool      = "Allow all (/yolo) waives this guard when you are there to answer; an allow rule and the other modes do not"
	stopProtected = "Allow all (/yolo) waives the protected-path prompt; the other modes and allow rules do not (\"protected_paths\" in the config only adds paths)"
	stopEgress    = "add the host to \"egress.allow\" in the config file, or set \"egress.default\" to allow; no mode skips it"
	stopTaint     = "no mode or rule skips it; it ends with /clear or a new session, and for a network command a yes covers that host for the session"
)

// setAsk records what made a call ask and how to stop it; the first cause
// stands.
func setAsk(call *ToolCall, src, why, stop string) {
	if call.Source == "" && call.Why == "" {
		call.Source, call.Why, call.Stop = src, why, stop
	}
}

// toolGuardWhy words the tool's own guard (a command that reaches outside
// the workspace, a destructive one-liner, one that could not be graded).
func toolGuardWhy(why string) string {
	if why == "" {
		return "the tool's own guard"
	}
	return "the tool's own guard: " + strings.TrimPrefix(why, "this ")
}

// modeAsk fills Why and Stop for a call no layer but the permission mode
// asked about. Source stays empty: the mode alone asked.
func (a *Agent) modeAsk(call *ToolCall) {
	if call.Why != "" {
		return
	}
	s := a.Settings()
	switch {
	case s.AutoMode:
		call.Why = "permission mode Auto: auto-approve does not cover this call"
		call.Stop = "choose Always allow to save the suggested rule, or switch to Allow all (/yolo)"
	case s.AutoEdits:
		call.Why = "permission mode Edits: file edits are approved, commands and other tools ask"
		call.Stop = "choose Always allow to save the suggested rule, or switch to Auto (/auto) or Allow all (/yolo)"
	default:
		call.Why = "permission mode Ask: edits and commands ask"
		call.Stop = "choose Always allow to save the suggested rule, or switch mode: Edits (/edits), Auto (/auto) or Allow all (/yolo)"
	}
	if a.Policy != nil && a.Policy.ManagedRulesOnly {
		call.Stop = "a managed policy allows only its own allow rules, so Always allow covers one call; ask whoever manages it"
	}
}

// Explain is the one-line form the prompts show: "Asked because …; to
// stop: …". Empty when the call carries no explanation.
func (c ToolCall) Explain() string {
	if c.Why == "" {
		return ""
	}
	s := "Asked because " + strings.TrimSuffix(c.Why, ".")
	if c.Stop != "" {
		s += "; to stop: " + strings.TrimSuffix(c.Stop, ".")
	}
	return s
}

// LastAsk is the call most recently put to the user, for /permissions why.
func (a *Agent) LastAsk() (ToolCall, bool) {
	if c := a.lastAsk.Load(); c != nil {
		return *c, true
	}
	return ToolCall{}, false
}

// WhyLastAsk is the /permissions why answer.
func (a *Agent) WhyLastAsk() string {
	c, ok := a.LastAsk()
	if !ok {
		return "nothing has asked yet this session"
	}
	out := "last ask: " + c.Name
	if c.Warning != "" {
		out += " (" + c.Warning + ")"
	}
	return out + "\n" + c.Explain()
}
