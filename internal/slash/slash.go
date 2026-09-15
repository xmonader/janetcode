// Package slash is the one table of slash commands. The terminal, the web
// UI and the feature catalogue all read it, so a command's name, argument
// shape and help are written once. The when-it-helps lines live with the
// catalogue, in internal/feature. It imports nothing from janetcode.
package slash

// Command is one built-in slash command.
type Command struct {
	Name string `json:"name"`
	Args string `json:"args"`
	Help string `json:"help"`
	// Idle marks a command that waits for the running turn to finish.
	Idle bool `json:"idle,omitempty"`
}

// Commands is every built-in command, in the order /help lists them.
var Commands = withIdle([]Command{
	// No args here on purpose: enter on /help in the menu should print
	// the sheet, not wait for a topic. /help <topic> still works typed.
	{Name: "/help", Args: "", Help: "keys and commands, or /help <topic> to search every feature and the manual"},
	{Name: "/compact", Args: "", Help: "summarise the conversation to free context"},
	{Name: "/clear", Args: "", Help: "start a new session"},
	{Name: "/resume", Args: "[n|id]", Help: "reopen an earlier session here: alone opens a searchable list, or name one"},
	{Name: "/open", Args: "<path[:line]>", Help: "show a file inline, centred on a line"},
	{Name: "/diff", Args: "[path|revert path:n]", Help: "changes against HEAD; a file's hunks; revert one hunk"},
	{Name: "/fork", Args: "[stay] [title]", Help: "copy this conversation into a new session; stay keeps this one live"},
	{Name: "/add-dir", Args: "<path>", Help: "grant file tools another local directory for this session"},
	{Name: "/btw", Args: "<question>", Help: "ask a side question that is not stored as a user turn"},
	{Name: "/copy", Args: "", Help: "copy the last assistant reply to the clipboard"},
	{Name: "/config", Args: "[key=value]", Help: "set a catalogued config key, or list settable keys"},
	{Name: "/doctor", Args: "", Help: "run a setup checkup; reports facts, changes nothing"},
	{Name: "/title", Args: "<text>", Help: "name this session (titles are generated after the first turn)"},
	{Name: "/history", Args: "<words>", Help: "find earlier sessions in any directory whose conversation mentions the words"},
	{Name: "/find", Args: "<text>", Help: "jump to the next line matching text; repeat to continue"},
	{Name: "/stats", Args: "", Help: "session statistics: turns, model calls, tool calls per tool, tokens, cost"},
	{Name: "/undo", Args: "[preview | n | about <description> | apply [--force]]", Help: "revert everything the last turn changed (git snapshot; file copies outside git); preview only lists it; n reverts one change from /changes; about <description> previews only the changes matching it, apply reverts that preview (--force if the preview has mixed changes or a coupling warning)"},
	{Name: "/changes", Args: "[n]", Help: "this session's file changes grouped by turn and intent; n shows one change's hunks"},
	{Name: "/blame", Args: "<path>", Help: "which change wrote each line of a file changed this session"},
	{Name: "/redo", Args: "", Help: "reapply what the last /undo reverted (until the next change)"},
	{Name: "/mcp", Args: "[trust|auth <server>|logout <server>]", Help: "MCP servers and their tools; trust starts the project's .mcp.json servers; auth logs in to a remote server with OAuth"},
	{Name: "/hooks", Args: "[trust]", Help: "hooks in effect; trust enables the project's .janetcode/hooks.json"},
	{Name: "/permissions", Args: "[allow|deny <rule>|trust]", Help: "permission rules; add one, or trust the project's .janetcode/permissions.json"},
	{Name: "/verify", Args: "[set <cmd>|add <cmd>|mutate <test cmd>|clear|trust]", Help: "run the project's checks now; set replaces .janetcode/verify.json's commands with one, add appends one; mutate requires tests to catch mutations of changed lines"},
	{Name: "/lsp", Args: "", Help: "language servers and the diagnostics they reported"},
	{Name: "/jobs", Args: "[id|kill <n>|stdin <n> <text>]", Help: "live overlay of background jobs; enter tails the log, k kills, stdin writes a line to a running job"},
	{Name: "/context", Args: "", Help: "what fills the context window: prompt, tools, messages, largest tool results"},
	{Name: "/todos", Args: "", Help: "live overlay of the plan: every step and its status; esc closes"},
	{Name: "/inspect", Args: "[subject]", Help: "look at any part of the session: context, events, archive, retrieval, config, tools, lsp, mcp, jobs, repomap… (no subject lists them)"},
	{Name: "/reload", Args: "", Help: "rebuild the memory and skill indexes in the prompt now instead of at compaction"},
	{Name: "/pin", Args: "[note or @file]", Help: "keep a note in the system prompt for the whole session; alone lists pins"},
	{Name: "/unpin", Args: "<n>", Help: "remove a pin"},
	{Name: "/plan", Args: "[request|approve|off]", Help: "plan mode: the model explores and writes a plan; approve executes it"},
	{Name: "/accept", Args: "[<request> | approve | lock | unlock | command <cmd> | off]", Help: "acceptance tests you approve first: writes tests for a request and stops; approve locks them so no tool call can weaken them and their command gates the session; unlock/lock and off manage the mode; command replaces the verify command when the stated or inferred one is wrong"},
	{Name: "/trajectory", Args: "[n|grep <words>]", Help: "event ledger of this session (turn/step/tool/compaction); n shows one record"},
	{Name: "/why", Args: "[n]", Help: "explain the last thing the harness did on its own, in plain words; n goes further back"},
	{Name: "/feedback", Args: "up|down [note]", Help: "rate the last assistant message (not sent to the model)"},
	{Name: "/goal", Args: "[objective|pause|resume|done]", Help: "set a goal and start on it now; the model keeps going round after round until it is complete or blocked"},
	{Name: "/agents", Args: "[id|new <brief>|fork <brief>]", Help: "live overlay of continuable subagents; enter (or /agents <id>) opens one child's transcript; new starts a general subagent, fork one that inherits this conversation"},
	{Name: "/ssh", Args: "[user@host[:/path]|off]", Help: "run file tools and bash on a remote host over ssh; off returns here"},
	{Name: "/docker", Args: "[container[:/path]|off]", Help: "run file tools and bash in a docker container; off returns here"},
	{Name: "/readonly", Args: "", Help: "toggle read-only mode (no edits or commands)"},
	{Name: "/memory", Args: "", Help: "what janetcode remembers about this project"},
	{Name: "/skills", Args: "", Help: "skill files available to the model"},
	{Name: "/rules", Args: "", Help: "path-scoped project rules: what brings each in, and which have been sent this session"},
	{Name: "/remember", Args: "<fact>", Help: "add a fact to the project memory"},
	{Name: "/forget", Args: "<n>", Help: "remove memory entry n"},
	{Name: "/yolo", Args: "", Help: "toggle auto-approve for tools"},
	{Name: "/auto", Args: "", Help: "toggle auto mode: in-workspace edits and known-safe reads run, other commands ask"},
	{Name: "/edits", Args: "", Help: "toggle auto-accept edits: file changes go through, commands still ask"},
	{Name: "/sandbox", Args: "[on|off|workspace|read-only|strict|backend|image]", Help: "run local bash under a sandbox profile (docker, seatbelt or Landlock); off by default"},
	{Name: "/review", Args: "[focus]", Help: "fan out two subagents over the current diff (bugs and tests)"},
	{Name: "/loop", Args: "[duration] [prompt|off|cancel id]", Help: "session-scoped repeating prompt; restored on resume of this session"},
	{Name: "/settle", Args: "", Help: "mark this session done with (or active again); settled sessions list last in /resume"},
	{Name: "/connect", Args: "", Help: "connect to a provider (preset or custom API) and pick a model"},
	{Name: "/model", Args: "[name]", Help: "pick a model from a list, or set one by name"},
	{Name: "/usage", Args: "", Help: "tokens and cost for this session"},
	{Name: "/budget", Args: "[usd|off]", Help: "stop the session once it has spent this much; alone says what the ceiling is and what is left"},
	{Name: "/export", Args: "[file.md]", Help: "write this session as a Markdown transcript"},
	{Name: "/theme", Args: "[name]", Help: "switch the colour theme: alone opens a searchable list, or name a builtin or user theme; the choice is saved"},
	{Name: "/verbose", Args: "", Help: "toggle expanded tool output for the whole transcript (ctrl+o expands one turn)"},
	{Name: "/hints", Args: "[on|off]", Help: "one-time hints about features this session could have used; alone says how many have been given"},
	{Name: "/eval", Args: "keep [name] [--check cmd] [--base rev] [--self-contained]", Help: "keep turns this session into a regression eval task under .janetcode/evals, runnable later with janetcode --eval; --check sets its check command, else the project's verify.json"},
	{Name: "/recipe", Args: "[list | show <name> | save <name> [--turn n] [k=v...] | run <name> [--dry-run] [k=v...]]", Help: "crystallise a turn's tool calls into a replayable program under .janetcode/recipes and run it later with no model call; a failed step hands over to the model"},
	{Name: "/proof", Args: "[commit | push [remote] | fetch [remote]]", Help: "which session, turn and intent wrote each file of a commit, and what actually demonstrated it worked (mutation score included when logged); attaches one from this session first if the commit (default HEAD) has none yet; push/fetch move notes to/from a remote, since they are local only until pushed"},
	{Name: "/quit", Args: "", Help: "exit"},
})

// idleOnly names the commands that cannot run while a turn is: they swap
// the conversation or the backend under the turn's feet. Resolving one
// mid-turn is blocked with one message, in one place.
// /budget is here for a different reason from the rest: the ceiling is
// read by the turn that is running, so writing it from the key handler
// while a turn is in flight would be a data race on the agent.
var idleOnly = []string{"/compact", "/clear", "/new", "/resume", "/sessions", "/fork", "/connect", "/model", "/undo", "/redo", "/budget", "/ssh", "/docker", "/eval", "/recipe"}

// withIdle sets Idle on every command idleOnly names. It runs as part of
// initialising Commands, so no reader ever sees the table without it.
func withIdle(cmds []Command) []Command {
	idle := map[string]bool{}
	for _, n := range idleOnly {
		idle[n] = true
	}
	for i := range cmds {
		cmds[i].Idle = idle[cmds[i].Name]
	}
	return cmds
}

// Lookup finds a command by its table name (aliases are not resolved).
func Lookup(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// Canonical resolves an alias to the command it names.
func Canonical(name string) string {
	if c := Aliases[name]; c != "" {
		return c
	}
	return name
}

// Aliases are the second names the dispatch switch answers to.
// They are not separate features, so they need no catalogue entry.
var Aliases = map[string]string{
	"/?": "/help", "/exit": "/quit", "/q": "/quit", "/new": "/clear",
	"/perms": "/permissions", "/ctx": "/context", "/todo": "/todos",
	"/i": "/inspect", "/search": "/find", "/sessions": "/resume",
	"/cost": "/usage",
}
