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
	// KeyHelp is the clause of the description that names a terminal
	// key (" alt+e cycles it"). The terminal appends it with the key the
	// user bound; the web UI has none of these keys and leaves it off.
	KeyHelp string `json:"-"`
	// Idle marks a command that waits for the running turn to finish.
	Idle bool `json:"idle,omitempty"`
}

// Commands is every built-in command, in the order /help lists them.
var Commands = withIdle([]Command{
	// No args here on purpose: enter on /help in the menu should print
	// the sheet, not wait for a topic. /help <topic> still works typed.
	{Name: "/help", Args: "", Help: "the everyday keys and commands; all lists every one; /help <topic> searches every feature and the manual"},
	{Name: "/compact", Args: "", Help: "summarise the conversation to free context"},
	{Name: "/compact-fast", Args: "", Help: "prune old tool results and thinking without a summary call"},
	{Name: "/clear", Args: "", Help: "start a new session"},
	{Name: "/recover", Args: "", Help: "carry on the turn this session died in: finished tool results kept, running reads rerun, running changes described, not repeated"},
	{Name: "/resume", Args: "[n|id] [fork]", Help: "reopen an earlier session here: alone opens a searchable list, or name one; fork continues in a copy, the way on when another janetcode has it open"},
	{Name: "/open", Args: "<path[:line]>", Help: "show a file inline, centred on a line"},
	{Name: "/diff", Args: "[path|revert path:n]", Help: "changes against HEAD; a file's hunks; revert one hunk"},
	{Name: "/fork", Args: "[stay] [title]", Help: "copy this conversation into a new session; stay keeps this one live"},
	{Name: "/add-dir", Args: "<path>", Help: "grant file tools another local directory for this session"},
	{Name: "/btw", Args: "<question>", Help: "ask a side question that is not stored as a user turn"},
	{Name: "/recap", Args: "", Help: "a short \"where was I\" summary of the session so far; shown to you only, never added to the conversation (recap_idle_minutes writes one on its own when you return from a break)"},
	{Name: "/copy", Args: "", Help: "copy the last assistant reply to the clipboard"},
	{Name: "/config", Args: "[key=value]", Help: "set a catalogued config key, or list settable keys"},
	{Name: "/doctor", Args: "", Help: "run a setup checkup; reports facts, changes nothing"},
	{Name: "/title", Args: "<text>", Help: "name this session (titles are generated after the first turn)"},
	{Name: "/history", Args: "<words>", Help: "find earlier sessions in any directory whose conversation mentions the words"},
	{Name: "/find", Args: "<text>", Help: "jump to the next line matching text; repeat to continue"},
	{Name: "/stats", Args: "", Help: "session statistics: turns, model calls, tool calls per tool, tokens, cost"},
	{Name: "/perf", Args: "", Help: "where the time went: model, tools, waiting on you, the harness itself"},
	{Name: "/undo", Args: "[preview | n | turn <n> [--files-only] | about <description> | apply [--force]]", Help: "revert everything the last turn changed (git snapshot; file copies outside git); preview only lists it; n reverts one change from /changes; turn <n> previews putting the whole work tree back to before that turn and rewinding the conversation with it (--files-only keeps the conversation); about <description> previews only the changes matching it; apply carries out the last preview (--force if it has mixed changes or a coupling warning)"},
	{Name: "/changes", Args: "[n]", Help: "this session's file changes grouped by turn and intent; n shows one change's hunks"},
	{Name: "/tour", Args: "[turn] [all]", Help: "review a turn's changes riskiest hunk first, each with its stated intent; reformatting and renames collapsed behind a count (all shows them); ends with the comprehension debt"},
	{Name: "/blame", Args: "<path>", Help: "which change wrote each line of a file changed this session"},
	{Name: "/redo", Args: "", Help: "reapply what the last /undo reverted (until the next change)"},
	{Name: "/mcp", Args: "[search <words>|add [<name> <command|url>]|remove <server>|reconnect [server]|trust|auth <server>|logout <server>]", Help: "MCP servers and their tools; search looks one up in the MCP registry and prints the add line; add writes one into .mcp.json and starts it (no arguments walks through it); reconnect restarts a server now (all that are down, with no name); trust starts the project's .mcp.json servers; auth logs in to a remote server with OAuth"},
	{Name: "/import-claude", Args: "[mcp|permissions|skills|agents ...] | apply <id>", Help: "bring over what Claude Code has set up (~/.claude.json MCP servers, permission rules from settings.json, ~/.claude skills and agents): alone it previews and prints a plan id; apply <id> writes exactly that; nothing existing is replaced and secrets are never printed"},
	{Name: "/cua", Args: "[on|off|doctor]", Help: "start Cua Driver as an MCP desktop server when cua-driver is installed; doctor checks the binary"},
	{Name: "/egress", Args: "", Help: "what the last turn sent off this machine: hosts, bytes, files and tool output in the model requests, memory and skills attached, values redacted; and the egress policy"},
	{Name: "/trust", Args: "[full | instructions | untrusted] [session]", Help: "how far this project's own skills, agents, commands, rules, hooks, MCP servers, verify commands, allow rules and recipes are let in; session holds the level only until exit"},
	{Name: "/hooks", Args: "[trust]", Help: "hooks in effect; trust enables the project's .janetcode/hooks.json"},
	{Name: "/permissions", Args: "[allow|deny|ask <rule>|check <command>|trust]", Help: "permission rules; add one (ask: always asks, in every mode), check which rule decides a call, or trust the project's .janetcode/permissions.json"},
	{Name: "/verify", Args: "[set <cmd>|add <cmd>|mutate <test cmd>|clear|trust]", Help: "run the project's checks now; set replaces .janetcode/verify.json's commands with one, add appends one; mutate requires tests to catch mutations of changed lines"},
	{Name: "/lsp", Args: "", Help: "language servers and the diagnostics they reported"},
	{Name: "/jobs", Args: "[id|kill <n>|stdin <n> <text>]", Help: "live overlay of background jobs; enter tails the log, k kills, stdin writes a line to a running job"},
	{Name: "/context", Args: "", Help: "what fills the context window: prompt, tools, messages, largest tool results"},
	{Name: "/todos", Args: "", Help: "live overlay of the plan: every step and its status; esc closes"},
	{Name: "/inspect", Args: "[subject]", Help: "look at any part of the session: context, events, archive, retrieval, config, tools, lsp, mcp, jobs, repomap… (no subject lists them)"},
	{Name: "/reload", Args: "", Help: "rebuild the memory and skill indexes in the prompt now instead of at compaction"},
	{Name: "/pin", Args: "[note or @file]", Help: "keep a note in the system prompt for the whole session; alone lists pins"},
	{Name: "/unpin", Args: "<n>", Help: "remove a pin"},
	{Name: "/plan", Args: "[request | review | comment <line> <text> | changes | approve | off]", Help: "plan mode: the model explores and writes a plan to .janetcode/plan.md; review reads it with line numbers, comment leaves a remark on a line, changes sends every comment back and stays in plan mode, approve executes it"},
	{Name: "/accept", Args: "[<request> | approve | lock | unlock | command <cmd> | off]", Help: "acceptance tests you approve first: writes tests for a request and stops; approve locks them so no tool call can weaken them and their command gates the session; unlock/lock and off manage the mode; command replaces the verify command when the stated or inferred one is wrong"},
	{Name: "/trajectory", Args: "[n | all | turns | turn <n> | slow [n] | grep <words> | raw]", Help: "browse the session as a ledger with a timing strip; an argument asks for one view in text"},
	{Name: "/why", Args: "[n]", Help: "explain the last thing the harness did on its own, in plain words; n goes further back"},
	{Name: "/feedback", Args: "up|down [note]", Help: "rate the last assistant message (not sent to the model)"},
	{Name: "/goal", Args: "[objective|rounds=N objective|rounds N|pause|resume|done]", Help: "set a goal and start on it now; the model keeps going round after round until it is complete, blocked, or out of rounds (8 by default; rounds=N sets the budget, rounds N gives a stopped goal more)"},
	{Name: "/agents", Args: "[id|new <brief>|fork <brief>|cancel <id>|steer <id> <note>]", Help: "live overlay of continuable subagents; enter (or /agents <id>) opens one child's transcript; new starts a general subagent, fork inherits this conversation, cancel stops a running one, steer injects a note before its next model call"},
	{Name: "/cd", Args: "[path]", Help: "move this session's working directory without restarting; no argument says where it is"},
	{Name: "/ssh", Args: "[user@host[:/path]|off]", Help: "run file tools and bash on a remote host over ssh; off returns here"},
	{Name: "/docker", Args: "[container[:/path]|off]", Help: "run file tools and bash in a docker container; off returns here"},
	{Name: "/readonly", Args: "", Help: "toggle read-only mode (no edits or commands)"},
	{Name: "/checkpoints", Args: "", Help: "the restore points: each turn that changed something, what its checkpoint covers (whole tree or file copies), whether it is complete; enter previews restoring to before it", KeyHelp: " (esc esc opens it too)"},
	{Name: "/dryrun", Args: "[on|off|show|apply [n,m…]|discard]", Help: "dry run: the model plans and edits a preview, commands that change things are recorded but not run; show reviews it with diffs, apply writes the files (undoable), discard drops them"},
	{Name: "/memory", Args: "[words | show <name> | about <path> | history <name> | current]", Help: "what janetcode remembers about this project"},
	{Name: "/dream", Args: "", Help: "consolidate overlapping auto-memory entries; does not touch what you /remembered"},
	{Name: "/sleep", Args: "", Help: "read this session's record and store what it established, with evidence; runs on its own when a goal finishes"},
	{Name: "/init", Args: "[force]", Help: "write the project's starter instruction file from what this repository shows: languages, declared commands, layout; force overwrites an existing one"},
	{Name: "/skills", Args: "", Help: "skill files available to the model, what each costs in tokens and which have fired this session"},
	{Name: "/curator", Args: "[list | pin <name> | archive <name> | restore <name>]", Help: "pin, archive or restore auto-generated skills"},
	{Name: "/followup", Args: "[on|off]", Help: "ghost-text next-prompt after a turn", KeyHelp: "; tab accepts"},
	{Name: "/rules", Args: "", Help: "path-scoped project rules: what brings each in, and which have been sent this session"},
	{Name: "/remember", Args: "<fact>", Help: "add a fact to the project memory"},
	{Name: "/forget", Args: "<n>", Help: "remove memory entry n"},
	{Name: "/yolo", Args: "", Help: "toggle Allow all tools (yolo): every tool runs without asking"},
	{Name: "/auto", Args: "", Help: "toggle auto mode: in-workspace edits and known-safe reads run, other commands ask"},
	{Name: "/edits", Args: "", Help: "toggle auto-accept edits: file changes go through, commands still ask"},
	{Name: "/sandbox", Args: "[on|off|workspace|read-only|strict|backend|image]", Help: "run local bash under a sandbox profile (docker, seatbelt or Landlock); off by default"},
	{Name: "/plugin", Args: "[list|show <name>|install <git url|dir|name@market>|trust <name>|enable <name>|disable <name>|update [name]|remove <name>|marketplace add|list|update|remove|browse]", Help: "plugins: bundles of commands, skills, agent profiles, hooks and MCP servers (Claude Code's layout); install fetches one, its hooks and servers stay off until trust; marketplace add registers an index to browse and install name@market from"},
	{Name: "/review", Args: "[focus]", Help: "fan out two subagents over the current diff (bugs and tests)"},
	{Name: "/security-review", Args: "[focus]", Help: "fan out two read-only subagents over the current diff with a security brief (injection, path traversal, request forgery, access checks, secrets, crypto) and rank what they find by severity"},
	{Name: "/pr-comments", Args: "[n]", Help: "fetch a pull request's description, reviews and comments with gh (the current branch's by default) and share them with the model"},
	{Name: "/issue", Args: "<n>", Help: "fetch a GitHub issue with gh and share it with the model"},
	{Name: "/loop", Args: "[duration] [prompt|off|cancel id]", Help: "session-scoped repeating prompt; restored on resume of this session"},
	{Name: "/settle", Args: "", Help: "mark this session done with (or active again); settled sessions list last in /resume"},
	{Name: "/connect", Args: "", Help: "connect to a provider (preset or custom API) and pick a model"},
	{Name: "/model", Args: "[name | cheap|strong|advisor|vision [name|off]]", Help: "pick a model from a list, or set one by name; a slot name picks the model that slot uses, and an unset slot is the session's own"},
	{Name: "/routing", Args: "", Help: "toggle step routing for this session: while off, every model call goes to the session's own model"},
	{Name: "/effort", Args: "[tier|up|down]", Help: "how hard the model thinks: low, medium, high, xhigh, max (Anthropic models); alone reports the tier and whether the provider takes one; up and down step it", KeyHelp: "; alt+e cycles it (the keybindings config key can move it)"},
	{Name: "/usage", Args: "", Help: "tokens and cost for this session"},
	{Name: "/budget", Args: "[usd|off]", Help: "stop the session once it has spent this much; alone says what the ceiling is and what is left"},
	{Name: "/export", Args: "[file.md]", Help: "write this session as a Markdown transcript"},
	{Name: "/theme", Args: "[name]", Help: "switch the colour theme: alone opens a searchable list, or name a builtin or user theme; the choice is saved"},
	{Name: "/verbose", Args: "", Help: "toggle expanded tool output for the whole transcript", KeyHelp: " (ctrl+o expands one turn)"},
	{Name: "/vim", Args: "[on|off]", Help: "toggle vim mode for the prompt: esc for normal mode (hjkl, w/b/e, 0/^/$, x, dd, dw, cw, ciw, D, C, p, u), i/a/I/A/o/O to insert; vim_mode in the config starts every session in it"},
	{Name: "/hints", Args: "[on|off]", Help: "one-time hints about features this session could have used; alone says how many have been given"},
	{Name: "/eval", Args: "keep [name] [--check cmd] [--base rev] [--self-contained]", Help: "keep turns this session into a regression eval task under .janetcode/evals, runnable later with janetcode --eval; --check sets its check command, else the project's verify.json"},
	{Name: "/recipe", Args: "[list | show <name> | save <name> [--turn n] [k=v...] | run <name> [--dry-run] [k=v...]]", Help: "crystallise a turn's tool calls into a replayable program under .janetcode/recipes and run it later with no model call; a failed step hands over to the model"},
	{Name: "/commit", Args: "[message]", Help: "commit the files this session changed (nothing else, and nothing that looks like a credential) with a message drafted from the diff and the changes' intents, or with yours"},
	{Name: "/pr", Args: "[base]", Help: "push this branch and open a GitHub pull request onto base (default: the remote's default branch) with a drafted title and description; needs gh"},
	{Name: "/proof", Args: "[commit | push [remote] | fetch [remote] | verify [commit] | key]", Help: "which session, turn and intent wrote each file of a commit, and what actually demonstrated it worked (mutation score included when logged); attaches one from this session first if the commit (default HEAD) has none yet; push/fetch move notes to/from a remote, since they are local only until pushed; verify checks the commit's signed evidence (janetcode --proof verify also re-runs its commands); key prints this machine's evidence public key"},
	{Name: "/features", Args: "[name [on|off]]", Help: "the extra models janetcode can call on (a cheaper one for simple steps, a stronger one when stuck, a reviewer) and the features that use them; a name and on|off switches one and saves it"},
	{Name: "/jev", Args: "[on|off|model|provider|triage|intake|route|outcome [on|off]]", Help: "the optional Jev decision service, which judges failed checks and routes simple requests: its settings, what it has cost, and which of its uses are on; on|off switches it, model and provider open a list"},
	{Name: "/voice", Args: "[stop]", Help: "record a prompt from the microphone and put its transcript in the input; /voice again stops and transcribes. Needs arecord, sox or ffmpeg, and an OpenAI-compatible /audio/transcriptions endpoint (the voice.* config keys)"},
	{Name: "/quit", Args: "", Help: "exit"},
})

// idleOnly names the commands that cannot run while a turn is: they swap
// the conversation or the backend under the turn's feet. Resolving one
// mid-turn is blocked with one message, in one place.
// /budget is here for a different reason from the rest: the ceiling is
// read by the turn that is running, so writing it from the key handler
// while a turn is in flight would be a data race on the agent.
var idleOnly = []string{"/recover", "/compact", "/compact-fast", "/clear", "/new", "/resume", "/sessions", "/fork", "/connect", "/model", "/undo", "/redo", "/budget", "/cd", "/ssh", "/docker", "/eval", "/recipe", "/commit", "/pr", "/pr-comments", "/issue"}

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
	"/cost": "/usage", "/compress-fast": "/compact-fast",
}
