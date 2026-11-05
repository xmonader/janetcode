package tool

import (
	"strings"
	"sync"
)

// NudgeMarker starts every hint a Nudge appends; the eval harness counts
// results carrying it so an A/B of JANETCODE_OFF=toolnudge can see how
// often the hint fired.
const NudgeMarker = "\n(tip: use the "

// Nudge points a model that searches or reads through bash at the
// dedicated tool, in the bash RESULT rather than in the prompt. Live runs
// on a DeepSeek model called bash in every run and never called grep:
// the descriptions were not what kept it away, the habit and a deferred
// tool list were. A hint is appended at most once per tool per session,
// so it costs a line, not a nag. A nil Nudge is off.
type Nudge struct {
	// Loaded reports whether a tool's schema is in the request. A tool
	// that is deferred (lean_tools, headless) is named with the way to
	// load it; nil means every tool is listed.
	Loaded func(name string) bool

	mu    sync.Mutex
	shown map[string]bool
}

// Hint returns the line to append to a bash result for command, or "".
func (n *Nudge) Hint(command string) string {
	if n == nil {
		return ""
	}
	name, what := nudgeFor(command)
	if name == "" {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.shown[name] {
		return ""
	}
	if n.shown == nil {
		n.shown = map[string]bool{}
	}
	n.shown[name] = true
	h := NudgeMarker + name + " tool for this: " + what
	if n.Loaded != nil && !n.Loaded(name) {
		h += "; tool_search \"" + name + "\" loads it"
	}
	return h + ")"
}

// nudgeFor names the dedicated tool for a plain search or read command.
func nudgeFor(command string) (name, what string) {
	if _, _, ok := searchArgv(command); ok {
		return "grep", "same path:line:text, .gitignore respected, bounded output"
	}
	f := strings.Fields(command)
	if len(f) < 2 || strings.ContainsAny(command, "|;&><`$") {
		return "", ""
	}
	switch f[0] {
	case "find":
		return "glob", "pattern like \"**/*.go\", newest first, skips build dirs"
	case "cat":
		return "read_file", "numbered lines, line ranges, no re-reading unchanged files"
	}
	return "", ""
}
