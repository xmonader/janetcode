package tui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/feature"
)

// This file turns the two tables the terminal already runs on — the
// slash commands and the keys — into catalogue entries, so that
// --inspect features, /help <topic> and the model's introspect tool all
// describe the janetcode that is actually built rather than a list
// somebody remembered to update.
//
// The command table supplies the name, the argument shape and what the
// command does. The only thing it does not carry is when the command
// helps, which is the half a user cannot guess, so that line lives here,
// keyed by command name. TestEveryCommandIsCatalogued fails when a
// command has none.

// commandWhen is the "when it helps" line for each built-in command.
var commandWhen = map[string]string{
	"/help":        "you want the keys, or what a feature is called: /help <topic> searches the catalogue and the manual",
	"/compact":     "the context gauge is high and you would rather summarise now than mid-task",
	"/clear":       "the next thing you want is unrelated to this conversation",
	"/resume":      "you want to pick up a session from yesterday, in this directory or any other",
	"/open":        "an error names a file and a line and you want to see it without leaving the session",
	"/diff":        "before committing, or to throw away one hunk the model wrote",
	"/fork":        "the conversation is worth keeping but you want to try a second direction from here",
	"/title":       "a session you will want to find again by name",
	"/history":     "you remember what was said but not which session said it",
	"/find":        "the answer scrolled past and you want it back",
	"/stats":       "you want to know where the turns, the tool calls and the money went",
	"/undo":        "the model changed the wrong thing; this puts every file back as it was before the turn",
	"/changes":     "you want to review what this session wrote, turn by turn, before trusting it",
	"/blame":       "a file looks wrong and you want to know which turn wrote which line",
	"/redo":        "the undo was the mistake",
	"/mcp":         "a project ships .mcp.json, or a remote server needs a login",
	"/hooks":       "the project ships .janetcode/hooks.json and you want to see or enable it",
	"/permissions": "the same approval keeps coming up, or the project ships rules you want to trust",
	"/verify":      "you want the harness to run the build or the tests after every change and hand failures back to the model",
	"/lsp":         "an edit compiled in your head but the language server disagrees",
	"/jobs":        "a long command is running in the background and you want its output or to kill it",
	"/context":     "the gauge is filling and you want to know what is filling it before you compact",
	"/todos":       "a long task is running and you want to see the plan and what is left",
	"/inspect":     "something surprising happened and you want the state that produced it",
	"/reload":      "you just edited a skill, a rule or a memory entry and want the model to see it this turn",
	"/pin":         "a constraint the model keeps forgetting; a pin survives compaction",
	"/unpin":       "a pin has served its purpose and is now costing tokens every request",
	"/plan":        "a change big enough that you want to read the plan before any file is touched",
	"/trajectory":  "you want the raw record of the session: every turn, step, tool call and compaction",
	"/feedback":    "you want to mark an answer good or bad for later review; it is not sent to the model",
	"/goal":        "a job that will take many turns and you would rather not press enter for each one",
	"/agents":      "a subagent found something and you want to carry on the conversation with it",
	"/readonly":    "you want answers about the code with no chance of it being changed",
	"/memory":      "you want to see, or correct, what janetcode has learned about this project",
	"/skills":      "you want to know which skill files the model can reach from here",
	"/rules":       "a path-scoped rule should have fired and you want to know why it did not",
	"/remember":    "a fact you are tired of repeating: it comes back in every future session here",
	"/forget":      "a remembered fact has gone stale or was wrong",
	"/yolo":        "a long batch of work you have already decided to trust",
	"/edits":       "you trust the file changes but still want to see every command before it runs",
	"/settle":      "a finished session you want out of the way at the top of /resume",
	"/connect":     "first run, a new provider, or an API key that has changed",
	"/model":       "this task wants a cheaper, faster or stronger model than the last one",
	"/usage":       "you want the tokens and the money for this session alone",
	"/export":      "you want the conversation as a file to paste into a review or an issue",
	"/verbose":     "tool output is being folded and you want all of it for the whole transcript",
	"/quit":        "you are done",
	"/why":         "the harness did something you did not ask for and you want it explained without spending a model call",
	"/hints":       "the one-time hints are in your way, or you want them back",
}

// keyFeatures are the keys worth naming in the catalogue: the ones that
// do something a user would otherwise ask for by name. Keys that only
// move the cursor are not features.
var keyFeatures = []feature.Feature{
	{Name: "ctrl+g", Kind: feature.KindKey, Manual: "interface",
		What:   "move the command running in the foreground to the background, as a job, and let the turn continue",
		When:   "a build or a test suite is taking minutes and the turn is blocked on it",
		Topics: []string{"background", "job", "slow", "hang", "stuck", "promote"}},
	{Name: "ctrl+o", Kind: feature.KindKey, Manual: "interface",
		What:   "expand the last turn's folded tool output (again folds it)",
		When:   "a tool result was truncated on screen and you want the rest",
		Topics: []string{"expand", "output", "folded", "truncated"}},
	{Name: "ctrl+s", Kind: feature.KindKey, Manual: "interface",
		What:   "hand what you are typing to the running turn instead of queueing it",
		When:   "the model is going the wrong way and you want to correct it now, not after the turn",
		Topics: []string{"steer", "interrupt", "correct", "queue"}},
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

// commandInvoke overrides what the catalogue says to type, for the few
// commands whose argument is not in the table. /help takes a topic but
// carries no args, so that enter on it in the command menu prints the
// sheet instead of waiting for one.
var commandInvoke = map[string]string{
	"/help": "/help [topic]",
}

// commandTopics adds search words for commands whose name does not
// contain the word a user would search for. It is deliberately short:
// the description already carries most of the vocabulary.
var commandTopics = map[string][]string{
	"/compact":     {"context", "full", "window", "summarise", "shrink", "long", "output"},
	"/context":     {"window", "full", "tokens", "gauge", "budget"},
	"/undo":        {"revert", "checkpoint", "snapshot", "mistake", "restore"},
	"/verify":      {"test", "tests", "build", "gate", "check", "ci", "lint"},
	"/remember":    {"memory", "persist", "across", "sessions", "learn", "recall"},
	"/memory":      {"remember", "persist", "across", "sessions", "learn", "recall"},
	"/resume":      {"session", "sessions", "continue", "yesterday", "reopen"},
	"/jobs":        {"background", "long", "running", "kill"},
	"/permissions": {"approve", "allow", "deny", "rule", "trust", "prompt"},
	"/goal":        {"autonomous", "loop", "rounds", "keep", "going"},
	"/usage":       {"cost", "money", "price", "spend", "tokens", "budget"},
	"/stats":       {"cost", "money", "tokens", "turns", "counts"},
	"/model":       {"switch", "provider", "cheaper", "faster", "stronger"},
	"/why":         {"explain", "reason", "intervention", "harness", "did"},
	"/inspect":     {"state", "debug", "internals", "subject"},
	"/plan":        {"planning", "design", "approve", "before"},
}

// Features renders the terminal's commands and keys as catalogue
// entries. It reads the tables and nothing else, so it is safe to call
// before a session exists (--inspect features does).
func Features() []feature.Feature {
	out := make([]feature.Feature, 0, len(commands)+len(keyFeatures))
	for _, c := range commands {
		invoke := c.name
		switch {
		case commandInvoke[c.name] != "":
			invoke = commandInvoke[c.name]
		case c.args != "":
			invoke += " " + c.args
		}
		out = append(out, feature.Feature{
			Name:   c.name,
			Kind:   feature.KindCommand,
			What:   firstClause(c.help),
			When:   commandWhen[c.name],
			Invoke: invoke,
			Topics: commandTopics[c.name],
			Manual: "interface",
		})
	}
	return append(out, keyFeatures...)
}

// firstClause keeps a command's description to one line. A few entries
// in the table explain two things separated by a semicolon; the
// catalogue wants the first, and the manual has the rest.
func firstClause(help string) string {
	if i := strings.Index(help, "; "); i > 30 {
		return help[:i]
	}
	return help
}
