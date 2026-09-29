package tui

import (
	"github.com/xmonader/janetcode/internal/feature"
)

// The slash commands are catalogued by internal/slash, which the web UI
// shares. This file adds what only the terminal has: its keys.

// Features renders the commands and the terminal's keys as catalogue
// entries.
func Features() []feature.Feature {
	return append(feature.Commands(), keyFeatures...)
}

// keyFeatures are the keys worth naming in the catalogue: the ones that
// do something a user would otherwise ask for by name. Keys that only
// move the cursor are not features.
var keyFeatures = []feature.Feature{
	{Name: "ctrl+g", Kind: feature.KindKey, Manual: "interface",
		What:   "move the command running in the foreground to the background, as a job, and let the turn continue",
		When:   "a build or a test suite is taking minutes and the turn is blocked on it",
		Topics: []string{"background", "job", "slow", "hang", "stuck", "promote", "forever", "minutes", "waiting"}},
	{Name: "ctrl+o", Kind: feature.KindKey, Manual: "interface",
		What:   "expand the last turn's folded tool output (again folds it)",
		When:   "a tool result was truncated on screen and you want the rest",
		Topics: []string{"expand", "output", "folded", "truncated"}},
	{Name: "ctrl+s", Kind: feature.KindKey, Manual: "interface",
		What:   "hand what you are typing to the running turn instead of queueing it",
		When:   "the model is going the wrong way and you want to correct it now, not after the turn",
		Topics: []string{"steer", "interrupt", "correct", "queue"}},
	{Name: "shift+tab", Kind: feature.KindKey, Manual: "interface",
		What:   "cycle the session mode: ask (approvals), plan (read-only, write a plan), auto (edits and known-safe reads), yolo (tools run without asking)",
		When:   "you keep reaching for /plan, /auto and /yolo and want one key instead",
		Topics: []string{"mode", "plan", "auto", "yolo", "ask", "approve"}},
	{Name: "ctrl+t", Kind: feature.KindKey, Manual: "interface",
		What:   "open the overlay of continuable subagents (the same panel as /agents)",
		When:   "a subagent is running and you want to watch it without typing /agents",
		Topics: []string{"team", "subagent", "agents", "overlay"}},
	{Name: "ctrl+r", Kind: feature.KindKey, Manual: "interface",
		What:   "search the prompt history backwards as you type, this session's and earlier ones' in this directory; ctrl+r again finds an older match, enter keeps it in the composer (action history_search)",
		When:   "you typed a long prompt yesterday and want it back without pressing up forty times",
		Topics: []string{"history", "search", "recall", "previous", "prompt", "reverse"}},
	{Name: "ctrl+x ctrl+e", Kind: feature.KindKey, Manual: "interface",
		What:   "write the prompt in $VISUAL or $EDITOR; what you save comes back into the composer to send (action external_editor)",
		When:   "the prompt is long enough to want a real editor",
		Topics: []string{"editor", "vim", "emacs", "external", "compose", "long"}},
	{Name: "alt+e", Kind: feature.KindKey, Manual: "interface",
		What:   "cycle how hard the model thinks, up the ladder and round to the provider's default (/effort; action effort)",
		When:   "one step of a task needs more reasoning than the rest",
		Topics: []string{"effort", "thinking", "reasoning", "tier"}},
	{Name: "keybindings", Kind: feature.KindKey, Manual: "interface", Invoke: "the keybindings config key, /help keys",
		What:   "every prompt key is an action (submit, newline, cancel, history_search, external_editor, effort, mode_cycle, …) that the config file's keybindings object can move to other keys or unbind; /help keys shows the table in force",
		When:   "a key clashes with your terminal or tmux, or you want another binding",
		Topics: []string{"keybinding", "rebind", "remap", "shortcut", "keyboard", "custom", "keys"}},
	{Name: "esc", Kind: feature.KindKey, Manual: "interface",
		What:   "cancel the running turn, or clear the input when idle",
		When:   "the turn is wrong or is costing more than it is worth",
		Topics: []string{"cancel", "stop", "abort"}},
	{Name: "ctrl+c", Kind: feature.KindKey, Manual: "interface",
		What:   "cancel the turn; pressed twice when idle, quit",
		When:   "esc did not get you out",
		Topics: []string{"cancel", "quit", "exit"}},
	{Name: "ctrl+v", Kind: feature.KindKey, Manual: "interface",
		What:   "paste an image from the clipboard into the prompt",
		When:   "a screenshot says what a paragraph would not",
		Topics: []string{"paste", "image", "screenshot", "clipboard"}},
	{Name: "@path", Kind: feature.KindKey, Manual: "interface", Invoke: "@path",
		What:   "attach a file, an image or a directory listing to the prompt; tab completes the path",
		When:   "you are about to paste a long file or a stack trace: the reference costs a fraction of the paste",
		Topics: []string{"attach", "file", "paste", "reference", "mention"}},
	{Name: "!command", Kind: feature.KindKey, Manual: "interface", Invoke: "!command",
		What:   "run a shell command here and share its output with the model",
		When:   "you want the model to see a command's real output without it asking for permission to run it",
		Topics: []string{"shell", "run", "bash", "output"}},
	{Name: "r", Kind: feature.KindKey, Manual: "interface", Invoke: "r (at a permission prompt)",
		What:   "save a permission rule so calls of that shape never ask again",
		When:   "you have approved the same kind of call more than twice",
		Topics: []string{"permission", "approve", "rule", "always", "prompt"}},
	{Name: "ctrl+o transcript", Kind: feature.KindKey, Manual: "interface", Invoke: "click a tool line",
		What:   "fold or unfold one tool call or one thought in the transcript",
		When:   "you want one call's detail without turning on /verbose for everything",
		Topics: []string{"fold", "unfold", "detail"}},
}
