package slash

import "github.com/xmonader/janetcode/internal/feature"

// When is the "when it helps" line for each built-in command.
var When = map[string]string{
	"/help":        "you want the keys, or what a feature is called: /help <topic> searches the catalogue and the manual",
	"/compact":     "the context gauge is high and you would rather summarise now than mid-task",
	"/clear":       "the next thing you want is unrelated to this conversation",
	"/resume":      "you want to pick up a session from yesterday, in this directory or any other",
	"/open":        "an error names a file and a line and you want to see it without leaving the session",
	"/diff":        "before committing, or to throw away one hunk the model wrote",
	"/fork":        "the conversation is worth keeping but you want to try a second direction from here (/fork stay lists the copy and stays)",
	"/add-dir":     "a sibling tree or generated directory the file tools should be allowed to touch",
	"/btw":         "a question about the current work that should not become a turn in the log",
	"/copy":        "you want the last reply on the clipboard without selecting it",
	"/config":      "you want to flip a catalogued setting without opening a picker",
	"/doctor":      "something is off and you want the same checkup janetcode --doctor prints, without changing files",
	"/title":       "a session you will want to find again by name",
	"/history":     "you remember what was said but not which session said it",
	"/find":        "the answer scrolled past and you want it back",
	"/stats":       "you want to know where the turns, the tool calls and the money went",
	"/undo":        "the model changed the wrong thing; this puts every file back as it was before the turn, or /undo about <description> reverts only the changes that match it",
	"/changes":     "you want to review what this session wrote, turn by turn, before trusting it",
	"/blame":       "a file looks wrong and you want to know which turn wrote which line",
	"/redo":        "the undo was the mistake",
	"/mcp":         "a project ships .mcp.json, or a remote server needs a login",
	"/hooks":       "the project ships .janetcode/hooks.json and you want to see or enable it",
	"/permissions": "the same approval keeps coming up, or the project ships rules you want to trust",
	"/verify":      "you want the harness to run the build or the tests after every change and hand failures back to the model",
	"/lsp":         "an edit compiled in your head but the language server disagrees",
	"/jobs":        "a long command is running in the background and you want to watch its log or kill it",
	"/context":     "the gauge is filling and you want to know what is filling it before you compact",
	"/todos":       "a long task is running and you want every step of the plan in a live overlay",
	"/inspect":     "something surprising happened and you want the state that produced it",
	"/reload":      "you just edited a skill, a rule or a memory entry and want the model to see it this turn",
	"/pin":         "a constraint the model keeps forgetting; a pin survives compaction",
	"/unpin":       "a pin has served its purpose and is now costing tokens every request",
	"/plan":        "a change big enough that you want to read the plan before any file is touched",
	"/trajectory":  "you want the raw record of the session: every turn, step, tool call and compaction",
	"/feedback":    "you want to mark an answer good or bad for later review; it is not sent to the model",
	"/goal":        "a job that will take many turns and you would rather not press enter for each one",
	"/agents":      "a subagent is running or has finished and you want to watch it live, or you want to hand a brief to a new one now (/agents fork <brief> inherits this conversation)",
	"/ssh":         "the files you want the model to edit live on another machine you can reach with ssh",
	"/docker":      "the files you want the model to edit live in a running container",
	"/readonly":    "you want answers about the code with no chance of it being changed",
	"/memory":      "you want to see, or correct, what janetcode has learned about this project",
	"/skills":      "you want to know which skill files the model can reach from here",
	"/rules":       "a path-scoped rule should have fired and you want to know why it did not",
	"/remember":    "a fact you are tired of repeating: it comes back in every future session here",
	"/forget":      "a remembered fact has gone stale or was wrong",
	"/yolo":        "a long batch of work you have already decided to trust",
	"/auto":        "you trust in-workspace edits and read-only commands but still want to see the rest",
	"/edits":       "you trust the file changes but still want to see every command before it runs",
	"/sandbox":     "you want bash isolated in docker without retargeting file tools",
	"/review":      "you want two subagents to read the current diff before you commit",
	"/loop":        "a prompt you want repeated this session and restored when you resume it",
	"/settle":      "a finished session you want out of the way at the top of /resume",
	"/connect":     "first run, a new provider, or an API key that has changed",
	"/model":       "this task wants a cheaper, faster or stronger model than the last one",
	"/usage":       "you want the tokens and the money for this session alone",
	"/budget":      "you want the session to stop on its own once it has spent a figure, instead of watching the cost line",
	"/export":      "you want the conversation as a file to paste into a review or an issue",
	"/theme":       "the current colours are hard to read, or you want a different look; try them live and keep the one you like",
	"/verbose":     "tool output is being folded and you want all of it for the whole transcript",
	"/quit":        "you are done",
	"/why":         "the harness did something you did not ask for and you want it explained without spending a model call",
	"/hints":       "the one-time hints are in your way, or you want them back",
	"/eval":        "a session found or fixed something and you want it to stay caught: a regression test that replays it against this project",
	"/proof":       "months later, or in code review now: which session and turn wrote a committed line, what actually showed it worked (mutation score included), and push/fetch to share notes since they are local until moved",
	"/recipe":      "a turn's tool calls worked and you would rather replay them for free next time than pay a model to redo the same steps",
}

// Invoke overrides what the catalogue says to type, for the few
// commands whose argument is not in the table. /help takes a topic but
// carries no args, so that enter on it in the command menu prints the
// sheet instead of waiting for one.
var Invoke = map[string]string{
	"/help": "/help [topic]",
}

// Topics adds search words for commands whose name does not
// contain the word a user would search for. It is deliberately short:
// the description already carries most of the vocabulary.
var Topics = map[string][]string{
	"/compact":     {"context", "full", "window", "summarise", "shrink", "long", "output", "forgot", "forget", "lost"},
	"/context":     {"window", "full", "tokens", "gauge", "budget"},
	"/undo":        {"revert", "checkpoint", "snapshot", "mistake", "restore"},
	"/verify":      {"test", "tests", "build", "gate", "check", "ci", "lint"},
	"/remember":    {"memory", "persist", "across", "sessions", "learn", "recall", "forgot", "forget"},
	"/memory":      {"remember", "persist", "across", "sessions", "learn", "recall", "forgot", "forget"},
	"/resume":      {"session", "sessions", "continue", "yesterday", "reopen"},
	"/jobs":        {"background", "long", "running", "kill"},
	"/agents":      {"subagent", "subagents", "overlay", "task", "continue", "team", "teammate", "swarm", "fork", "start"},
	"/ssh":         {"remote", "host", "workspace", "server"},
	"/docker":      {"container", "remote", "workspace"},
	"/sandbox":     {"docker", "isolate", "container", "seatbelt"},
	"/review":      {"diff", "pr", "subagent", "bugs", "tests"},
	"/loop":        {"cron", "repeat", "schedule", "interval"},
	"/auto":        {"approve", "safe", "mode"},
	"/permissions": {"approve", "allow", "deny", "rule", "trust", "prompt"},
	"/goal":        {"autonomous", "loop", "rounds", "keep", "going"},
	"/usage":       {"cost", "money", "price", "spend", "tokens", "budget"},
	"/budget":      {"cost", "money", "spend", "ceiling", "limit", "max-cost"},
	"/stats":       {"cost", "money", "tokens", "turns", "counts"},
	"/model":       {"switch", "provider", "cheaper", "faster", "stronger"},
	"/why":         {"explain", "reason", "intervention", "harness", "did"},
	"/inspect":     {"state", "debug", "internals", "subject"},
	"/plan":        {"planning", "design", "approve", "before"},
	"/theme":       {"colour", "color", "palette", "dark", "light", "scheme", "contrast", "solarized", "gruvbox", "nord", "catppuccin", "dracula", "tokyo", "monokai", "kanagawa", "everforest"},
	"/add-dir":     {"directory", "root", "extra", "monorepo"},
	"/btw":         {"aside", "side", "question"},
	"/copy":        {"clipboard", "paste"},
	"/config":      {"setting", "key"},
	"/doctor":      {"checkup", "health", "diagnose"},
	"/eval":        {"regression", "test", "keep", "replay", "task"},
	"/proof":       {"evidence", "commit", "notes", "attribution", "audit", "provenance"},
	"/recipe":      {"replay", "script", "automation", "macro", "save", "run", "no model", "tokens"},
}

// Features renders the commands as catalogue entries. It reads the
// tables and nothing else, so it is safe to call before a session exists
// (--inspect features does). TestEveryCommandIsCatalogued fails when a
// command has no When line.
func Features() []feature.Feature {
	out := make([]feature.Feature, 0, len(Commands))
	for _, c := range Commands {
		invoke := c.Name
		switch {
		case Invoke[c.Name] != "":
			invoke = Invoke[c.Name]
		case c.Args != "":
			invoke += " " + c.Args
		}
		out = append(out, feature.Feature{
			Name:   c.Name,
			Kind:   feature.KindCommand,
			What:   firstClause(c.Help),
			When:   When[c.Name],
			Invoke: invoke,
			Topics: Topics[c.Name],
			Manual: "interface",
		})
	}
	return out
}

// firstClause keeps a command's description to one line. A few entries
// in the table explain two things separated by a semicolon; the
// catalogue wants the first, and the manual has the rest. The cut is
// feature.Headline's, which will not break a line inside a bracket.
func firstClause(help string) string { return feature.Headline(help, 31) }
