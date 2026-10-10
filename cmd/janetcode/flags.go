package main

import (
	"flag"
	"fmt"
	"github.com/xmonader/janetcode/internal/headless"
	"io"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

// options holds the value of every command-line flag janetcode declares.
// newFlagSet is the one place the command line is described, so the help
// text, the feature catalogue and the drift tests all read the same
// declarations rather than a copy of them.
type options struct {
	promptText   string
	outFormat    string
	jsonSchema   string
	inFormat     string
	maxCost      float64
	maxToolCalls int
	maxWallTime  time.Duration
	coreTools    string
	excludeTools string
	safeMode     bool
	debugFile    string
	listModels   bool
	maxTurns     int
	compactAt    int
	until        string
	watchCmd     string
	// branchRetry and secondSample are the opt-in second attempt
	// (internal/rollout): a branch from the critical step after a bad
	// end, a fresh sample after a green run on weak evidence.
	branchRetry  bool
	secondSample bool
	provider     string
	model        string
	effort       string
	yolo         bool
	autoMode     bool
	sandboxOn    bool
	readonly     bool
	dryRun       bool
	// trustLevel is --trust: the project trust level for this run only.
	trustLevel string
	continueC  bool
	// recoverTurn is --recover: carry on the turn a resumed session
	// died in (docs/notes/durable-turns.md).
	recoverTurn  bool
	resumeR      string
	autoEdits    bool
	evalPath     string
	evalJobs     int
	evalOnly     string
	evalJSON     string
	replayPath   string
	whatIf       string
	whatIfFrom   int
	whatIfOff    string
	whatIfPrompt string
	whatIfKeep   bool
	jsonOut      bool
	evalReport   string
	evalKeep     bool
	evalRuns     int
	evalCompare  string
	evalOff      string
	evalRoute    string
	evalLabel    string
	evalTrain    string
	evalTimeout  time.Duration
	evalTurns    int
	evalNoLimit  bool
	evalStrict   bool
	evalGate     bool
	inspectWhat  string
	wtBranch     string
	acpMode      bool
	mcpServe     bool
	pluginCmd    string
	evalFacts    int
	evalBlind    bool
	evalCompact  int
	allow        string
	deny         string
	ask          string
	cont         bool
	resume       string
	showVersion  bool
	plain        bool
	screenReader bool
	selfUpdate   bool
	uninstall    bool
	purge        bool
	listSess     bool
	// fork continues --resume/--continue in a copy of the session.
	fork bool
	// showSess prints a session's transcript read-only.
	showSess string
	// searchSess searches recorded sessions' transcripts.
	searchSess string
	// auditSess verifies a session's audit log.
	auditSess    string
	initProject  bool
	initCfg      bool
	mcpAuth      string
	appendSystem string
	runDoctor    bool
	backupTo     string
	answerID     string
	restoreFrom  string
	// decisionsReplay is Phase 0's offline calibration driver: a
	// labeled-state corpus path, see docs/notes/jev-integration-spec.md.
	decisionsReplay string
	// decisionsAnnotate labels recorded sessions offline (item 5 of the
	// surviving list): a path to a session file or a directory of them.
	// decisionsAnnotateOut is where the two output files land.
	decisionsAnnotate    string
	decisionsAnnotateOut string
	// sessionProfile is the offline call/token profiler: where a recorded
	// run's calls and input tokens went, with no decisions call at all.
	sessionProfile      string
	sessionProfileStale int
	doctorFix           bool
	runProof            bool
	runSessionRef       bool
	recipeName          string
	themeFlag           string
	web                 bool
	webAddr             string
	bg                  bool
	// addDir collects --add-dir, which is repeatable.
	addDir stringList
	// sets collects --set key=value, repeatable: the config keys /config
	// sets in a session, for the run that has no session to type in.
	sets stringList
}

// helpGroups orders --help by what a reader is trying to do, everyday
// first. A flag not listed in any group lands in "Advanced", so a new
// flag is never missing from the help, only filed there until someone
// decides it belongs higher up.
var helpGroups = []struct {
	title string
	names []string
}{
	{"Session", []string{"model", "provider", "effort", "c", "continue", "r", "resume", "sessions", "search", "show", "fork", "recover", "web", "web-addr", "worktree", "add-dir", "theme", "plain", "screen-reader"}},
	{"Headless (-p)", []string{"p", "output-format", "input-format", "json-schema", "bg", "answer", "max-turns", "max-cost", "max-tool-calls", "max-wall-time", "until", "branch-retry", "second-sample", "watch"}},
	{"Permissions", []string{"auto", "auto-edits", "readonly", "dry-run", "yolo", "allow", "deny", "ask", "core-tools", "exclude-tools", "safe-mode", "sandbox", "trust"}},
	{"Setup and diagnostics", []string{"init", "init-project", "doctor", "fix", "backup", "restore", "mcp-auth", "plugin", "update", "uninstall", "purge", "version", "debug", "list-models"}},
	{"Advanced", nil},
	{"Eval and benchmark (see docs/manual/evals.md)", nil},
}

// briefHelp is what plain --help shows: the flags a first week needs, one
// line each, so the help fits a screen. --help all prints every flag.
var briefHelp = []struct{ flag, arg, text string }{
	{"p", "prompt", "run one prompt headless, print the answer, exit"},
	{"c", "", "continue the newest session in this directory"},
	{"resume", "id", "reopen a session (--sessions lists them)"},
	{"model", "id", "the model to use; /model switches in a session"},
	{"provider", "name", "a configured provider or preset, e.g. openrouter"},
	{"web", "", "the same session in a browser instead of the terminal"},
	{"auto-edits", "", "file edits run without asking; commands still ask"},
	{"readonly", "", "the model can look but not change anything"},
	{"yolo", "", "run every tool call without asking"},
	{"bg", "", "run the prompt in the background; --answer settles its asks"},
	{"doctor", "", "check the config, the key and a live model call"},
	{"update", "", "install the latest release in place of this one"},
	{"version", "", "print the version"},
}

// printBriefHelp is plain --help.
func printBriefHelp(w io.Writer, fs *flag.FlagSet) {
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	fmt.Fprintf(w, `janetcode %s - a terminal coding agent

Usage: janetcode [flags] [prompt]      interactive session in this directory
       janetcode -p "prompt" [flags]    run one prompt headless and exit

`, version)
	for _, b := range briefHelp {
		name := "--" + b.flag
		if len(b.flag) == 1 {
			name = "-" + b.flag
		}
		if b.arg != "" {
			name += " " + b.arg
		}
		fmt.Fprintf(w, "  %-18s %s\n", name, b.text)
	}
	fmt.Fprintf(w, `
In a session, /help lists the commands. An API key comes from /connect,
or from ANTHROPIC_API_KEY / OPENAI_API_KEY.

janetcode --help all       every flag (%d), grouped, and the environment
docs/manual/cli.md         the same, with examples
`, n)
}

func helpGroupOf(name string) string {
	if name == "eval" || strings.HasPrefix(name, "eval-") {
		return helpGroups[len(helpGroups)-1].title
	}
	for _, g := range helpGroups {
		for _, n := range g.names {
			if n == name {
				return g.title
			}
		}
	}
	return "Advanced"
}

// newFlagSet declares janetcode's flags and returns the set together with
// the options struct the parsed values land in.
func newFlagSet() (*flag.FlagSet, *options) {
	fs := flag.NewFlagSet("janetcode", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `janetcode %s - a terminal coding agent

Usage: janetcode [flags] [prompt]      interactive session in this directory,
                                        optionally starting with a prompt
       janetcode -p "prompt" [flags]    run one prompt headless and exit
`, version)
		for _, g := range helpGroups {
			fmt.Fprintf(fs.Output(), "\n%s:\n", g.title)
			if g.names == nil {
				printFlags(fs, func(f *flag.Flag) bool { return helpGroupOf(f.Name) == g.title })
			}
			for _, n := range g.names {
				if f := fs.Lookup(n); f != nil {
					printFlag(fs, f)
				}
			}
		}
		fmt.Fprintf(fs.Output(), `
Environment:
  ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL
      a key may be a comma-separated list; they are rotated on rate limits
  ANTHROPIC_AUTH_TOKEN        the Anthropic key as a bearer token, for gateways
  JANETCODE_PROVIDER, JANETCODE_MODEL, JANETCODE_EFFORT, JANETCODE_THEME
  JANETCODE_OFF=<names>       disables behaviours for a session (see --eval-off)
  JANETCODE_SYSTEM_MD=<file>  replaces the base system prompt
  JANETCODE_DEBUG=<file>      appends a timestamped event log
  JANETCODE_PLAIN=1           ASCII interface, no animation (as --plain)
  JANETCODE_SCREEN_READER=1   screen-reader line interface (--screen-reader)
  JANETCODE_WEB_TOKEN=<token> the --web access token (default: random)
  JANETCODE_NOTIFY=<command>  run when a --bg run waits for --answer

Config file: $XDG_CONFIG_HOME/janetcode/config.json
             (default ~/.config/janetcode/config.json)
`)
	}
	o := &options{}
	fs.StringVar(&o.promptText, "p", "", "run this prompt headless and exit")
	fs.StringVar(&o.outFormat, "output-format", "text", "with -p: how the run reports itself. text prints the answer on stdout and activity on stderr; json prints one result object on stdout (answer, usage, cost and where the figure came from, turns, tool calls, permission denials, verdict, session id, duration); stream-json prints one JSON object per line as things happen, ending with that result. --doctor takes json too")
	fs.StringVar(&o.inFormat, "input-format", "text", "headless input. text: one prompt from -p. stream-json: a conversation on stdin, one JSON object per line ({\"type\":\"user\",\"text\":...} or {\"type\":\"control\",\"subtype\":\"interrupt\"}), a turn per message and a result line after each, until EOF; needs --output-format stream-json, and -p becomes optional")
	fs.StringVar(&o.jsonSchema, "json-schema", "", "with -p: answer with JSON matching this schema (inline, or @file). The model delivers it through a structured_output tool the harness validates; text format prints only the value on stdout, json and stream-json put it in structured_output. Exits 54 when no valid answer comes")
	fs.DurationVar(&o.maxWallTime, "max-wall-time", 0, "with -p: stop the run once it has taken this long (e.g. 10m), whatever it is doing (exit 56)")
	fs.IntVar(&o.maxToolCalls, "max-tool-calls", 0, "with -p: stop before the tool call that would take the run past this many (exit 57); the calls of the batch that would cross it are answered, not run")
	fs.Float64Var(&o.maxCost, "max-cost", 0, "with -p: stop once the run has spent this much in USD (exit 55), checked after every model call. The figure is the provider's when it reports one, otherwise list price")
	fs.IntVar(&o.maxTurns, "max-turns", 0, "cap the model calls one turn may make (0: the configured max_iterations). The same cap --eval-max-turns applies to an eval run, so a benchmark can give a headless run and an eval run the same budget")
	fs.IntVar(&o.compactAt, "compact-at", 0, "compact the history at this many estimated tokens instead of the configured value; the same knob --eval-compact-at applies to an eval run")
	fs.StringVar(&o.until, "until", "", "with -p: shell command that must exit 0 before the run is done (re-loops up to 5 times)")
	fs.BoolVar(&o.branchRetry, "branch-retry", false, "with -p: when the run ends badly (stuck, out of turns or tool calls, the project's checks or --until still red), branch once from the step where it stopped exploring: the steps before it are replayed from the recording at no cost, the rest run live with a line saying what failed, and the better of the two trees is kept. Opt-in: it buys a second run's calls")
	fs.BoolVar(&o.secondSample, "second-sample", false, "with -p: when the run finishes green on weak evidence (the verdict says the claim is not shown, the check is stale, or a gaming signal fired), draw one more attempt from the start and keep the better tree, ranked by the harness's own checks, never by a model. Opt-in: it can double the run's cost")
	fs.StringVar(&o.watchCmd, "watch", "", "watch mode: re-run this shell command whenever files change and have the model fix failures (use with --yolo)")
	fs.StringVar(&o.provider, "provider", "", "configured provider or preset name, e.g. anthropic, openai, openrouter, ollama")
	fs.StringVar(&o.model, "model", "", "model id (default claude-opus-5 for anthropic)")
	fs.StringVar(&o.effort, "effort", "", "anthropic effort: low, medium, high, xhigh, max")
	fs.BoolVar(&o.yolo, "yolo", false, "auto-approve every tool call")
	fs.BoolVar(&o.autoMode, "auto", false, "auto mode: in-workspace edits and known-safe reads run; other commands ask (/auto toggles it)")
	fs.BoolVar(&o.sandboxOn, "sandbox", false, "wrap local bash in the workspace sandbox profile (docker, seatbelt or Landlock); the sandbox config key picks another profile; file tools stay on the host")
	fs.StringVar(&o.trustLevel, "trust", "", "project trust for this run only: untrusted, instructions (skills, agents, commands, rules) or full (also hooks, MCP, verify, allow rules, recipes); JANETCODE_TRUST does the same")
	fs.BoolVar(&o.readonly, "readonly", false, "read-only: no edits or commands, the model can only inspect")
	fs.BoolVar(&o.dryRun, "dry-run", false, "dry run: the model plans and edits a preview, and commands that change things are recorded instead of run; nothing reaches the disk until /dryrun apply. With -p the preview (diffs, commands not run) is printed at the end, and is the dry_run field of --output-format json")
	fs.BoolVar(&o.continueC, "c", false, "short for --continue")
	fs.StringVar(&o.resumeR, "r", "", "short for --resume <id>")
	fs.BoolVar(&o.autoEdits, "auto-edits", false, "auto-accept file edits; commands still ask (/edits toggles it)")
	fs.StringVar(&o.evalPath, "eval", "", "run the eval tasks in this file or directory against the configured provider and exit")
	fs.IntVar(&o.evalJobs, "eval-jobs", 2, "eval tasks to run at once")
	fs.StringVar(&o.evalOnly, "eval-only", "", "comma-separated task names or tags to run")
	fs.StringVar(&o.evalJSON, "eval-json", "", "write eval results to this JSON file")
	fs.StringVar(&o.replayPath, "replay", "", "re-run a saved session against the current code, with the model's replies read from the recording instead of bought: reports the tool calls it makes now against the ones recorded, and needs no API key")
	fs.StringVar(&o.whatIf, "what-if", "", "counterfactual replay: replay this session (id or path) in an isolated git worktree. Turns before --from-turn re-run from the recording (no tokens spent); from there the session's remaining prompts run live against whatever changed (--model, --provider, --off, --deny, --prompt). Prints a comparison with the original run -- where they first diverged, per-turn verdicts, files changed, a check run in the worktree, tool calls, tokens and cost -- and exits 0; see docs/manual/sessions.md")
	fs.IntVar(&o.whatIfFrom, "from-turn", 0, "with --what-if: the first turn (1-based) to run live instead of from the recording")
	fs.StringVar(&o.whatIfOff, "off", "", "with --what-if: comma-separated behaviours to disable for the live part (same names as --eval-off / JANETCODE_OFF)")
	fs.StringVar(&o.whatIfPrompt, "prompt", "", "with --what-if: replacement text for --from-turn's prompt, instead of the session's own recorded one")
	fs.BoolVar(&o.whatIfKeep, "what-if-keep", false, "with --what-if: leave the worktree on disk instead of removing it, and print its path")
	fs.BoolVar(&o.jsonOut, "json", false, "with --what-if: write the comparison report as JSON to stdout instead of text")
	fs.StringVar(&o.evalReport, "eval-report", "", "re-read a saved results file and print its report and tool adoption, without running anything; with --eval-compare it prints the paired comparison of two saved files")
	fs.BoolVar(&o.evalKeep, "eval-keep", false, "keep eval work directories")
	fs.IntVar(&o.evalRuns, "eval-runs", 1, "repeat every eval task this many times (rates instead of coin flips). One run per task is a coin flip: this set swings 21% between identical runs, so a comparison wants 3 or more")
	fs.StringVar(&o.evalCompare, "eval-compare", "", "results file of a baseline run to compare against (paired by task, bootstrap interval)")
	fs.StringVar(&o.evalOff, "eval-off", "", "comma-separated agent behaviours to disable for an ablation run: "+strings.Join(agent.Switches(), ", "))
	fs.StringVar(&o.evalRoute, "eval-route", "", "set route_cheap for this run: send a turn's mechanical steps (reads, greps, checks that already passed) to this model instead of the session's, and record the variant in the results file so a comparison shows what it bought (see docs/manual/evals.md, \"Measuring step routing\")")
	fs.StringVar(&o.evalLabel, "eval-label", "", "name recorded in the results file for this variant")
	fs.StringVar(&o.evalTrain, "eval-train", "", "train the trajectory critic from these eval result files (comma-separated files or directories) and exit")
	fs.DurationVar(&o.evalTimeout, "eval-timeout", 0, "replace every task's timeout with this wall budget (benchmarks give each harness the same)")
	fs.IntVar(&o.evalTurns, "eval-max-turns", 0, "cap model calls per task run (0: the default of 50)")
	fs.BoolVar(&o.evalGate, "eval-gate", false, "run eval tasks under a verification gate: the work directory's .janetcode/verify.json, or the checks for its project type, run after a turn that changed something, and the configured decision provider (config \"decisions\", off with --eval-off decisions) may triage a failure. Off by default: a gate changes tool counts, pass rates and cost, so every baseline in this repository was recorded without one")
	fs.BoolVar(&o.evalNoLimit, "eval-no-tool-limit", false, "ignore the tasks' max_tool_calls, which other harnesses do not enforce")
	fs.BoolVar(&o.evalStrict, "eval-strict-provenance", false, "fail the whole eval when any run was answered by another vendor's model than the one requested, instead of leaving those runs out of the scores")
	fs.StringVar(&o.inspectWhat, "inspect", "", "print one part of the session state and exit: context, events, archive, retrieval, compaction, tools, config, lsp, mcp, jobs, skills, repomap and more; --inspect list names them all")
	fs.StringVar(&o.wtBranch, "worktree", "", "run the session in a git worktree on this branch, in a checkout under .git: the real tree is untouched, two sessions can work on one repository at once, and the result is a branch to review and merge")
	fs.StringVar(&o.pluginCmd, "plugin", "", "manage plugins from the shell, then exit: the arguments /plugin takes, e.g. --plugin \"install https://github.com/someone/kit\", --plugin list, --plugin \"trust kit\"")
	fs.BoolVar(&o.mcpServe, "mcp-serve", false, "serve janetcode as an MCP server on stdin/stdout: one tool, janetcode_task, runs a prompt here and returns the answer, session id, verdict and usage. Read-only unless --auto-edits, --auto or --yolo is given; what the mode would ask about is refused")
	fs.BoolVar(&o.acpMode, "acp", false, "serve the Agent Client Protocol on stdin/stdout, for an ACP editor that launches janetcode as a subprocess (nothing but protocol messages goes to stdout)")
	fs.IntVar(&o.evalFacts, "eval-facts", 0, "after each run that compacted, probe up to N facts mined from what compaction removed (exit codes, output lines, file lines) and record how many the model still recalls")
	fs.BoolVar(&o.evalBlind, "eval-facts-blind", false, "probe without expand_output: what the compacted context alone retains")
	fs.IntVar(&o.evalCompact, "eval-compact-at", 0, "compact the history at this many estimated tokens instead of the default: a low value forces compaction on tasks that would not otherwise reach it, to measure what it costs and what it loses")
	fs.StringVar(&o.allow, "allow", "", "comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'")
	fs.BoolVar(&o.safeMode, "safe-mode", false, "start with nothing from the project or your extensions: no hooks, MCP servers, verify gates, project permission rules, project instructions, skills, rules or agent profiles, and no auto-approval")
	fs.StringVar(&o.coreTools, "core-tools", "", "comma-separated tools the model is shown, and no others (globs allowed, e.g. 'read_file,grep,mcp__github__*')")
	fs.StringVar(&o.excludeTools, "exclude-tools", "", "comma-separated tools the model is never shown (globs allowed); unlike --deny, they are not in the request at all")
	fs.StringVar(&o.deny, "deny", "", "comma-separated permission rules to refuse for this run")
	fs.StringVar(&o.ask, "ask", "", "comma-separated sensitive-call rules for this run: what they match always asks, in every mode, e.g. '*(touches:**/.env)'")
	fs.BoolVar(&o.cont, "continue", false, "resume the latest session for this directory")
	fs.BoolVar(&o.recoverTurn, "recover", false, "with -c or --resume: carry on the turn the session died in (crash, kill, power loss) instead of sending a prompt; works with --bg")
	fs.StringVar(&o.resume, "resume", "", "resume the session with this id")
	fs.BoolVar(&o.showVersion, "version", false, "print version and exit")
	fs.BoolVar(&o.listModels, "list-models", false, "print the models the configured provider (or --provider) serves, with their context windows, then exit; --output-format json for a script")
	fs.StringVar(&o.debugFile, "debug", "", "append a timestamped timeline of model calls (duration, stop reason, usage), stream and tool events and MCP traffic to this file (the same as JANETCODE_DEBUG=<file>)")
	fs.BoolVar(&o.selfUpdate, "update", false, "replace this binary with the latest release, checked against its SHA256SUMS, then exit")
	fs.BoolVar(&o.uninstall, "uninstall", false, "remove this binary if janetcode installed it (install.sh or a release build), print what was removed, then exit; the config and data directories stay unless --purge is given")
	fs.BoolVar(&o.purge, "purge", false, "with --uninstall: also remove the config directory (settings, API keys, permission rules, MCP logins) and the data directory (sessions, memory, archives)")
	fs.BoolVar(&o.listSess, "sessions", false, "list saved sessions for this directory and exit")
	fs.BoolVar(&o.fork, "fork", false, "with --resume or --continue: continue in a copy of the session, leaving the original as it was -- the way on when another janetcode has it open")
	fs.StringVar(&o.showSess, "show", "", "print a saved session's conversation as Markdown and exit, without opening it: safe while another janetcode has it open")
	fs.StringVar(&o.auditSess, "audit", "", "check a session's audit log (the hash-chained record of every event: tool calls, what authorized each, the files they changed, model calls) against the session, print what it did turn by turn without running anything, then exit; exits 1 when the two do not agree")
	fs.StringVar(&o.searchSess, "search", "", "search every saved session's conversation for these words (all of them, any case) and exit: the matching sessions, newest first, with the turn and the line that matched")
	fs.BoolVar(&o.initCfg, "init", false, "write an example config file and exit")
	fs.BoolVar(&o.initProject, "init-project", false, "write a starter instruction file (the one read at the start of every session here) from what this repository shows -- languages, the commands it declares, its layout -- and exit; an existing one is never overwritten")
	fs.StringVar(&o.mcpAuth, "mcp-auth", "", "log in to the named remote MCP server with OAuth (browser flow) and exit")
	fs.StringVar(&o.appendSystem, "append-system-prompt", "", "text (or @file) appended to the system prompt; JANETCODE_SYSTEM_MD=<file> replaces the base prompt.")
	fs.BoolVar(&o.doctorFix, "fix", false, "with --doctor: carry out the safe repairs it lists (a misspelled config key, a missing config directory, an expired MCP login that cannot refresh, a directory of ours the owner cannot read) and print each change; anything needing judgement is still only reported, including an untrusted .janetcode/verify.json, which is listed with the /verify trust step for you to take")
	fs.StringVar(&o.decisionsAnnotate, "decisions-annotate", "", "label recorded sessions offline with the decisions model (a session .jsonl or a directory of them) and exit")
	fs.StringVar(&o.decisionsAnnotateOut, "decisions-annotate-out", "", "directory for --decisions-annotate's annotations.jsonl and critic-train.json (default: the working directory)")
	fs.StringVar(&o.sessionProfile, "session-profile", "", "classify the calls of recorded runs offline (an --eval-json result file, a session .jsonl, or a directory of either) as explore/edit/verify/other, report input-token growth and the share of context that is stale tool output, and exit")
	fs.IntVar(&o.sessionProfileStale, "session-profile-stale", profileStaleAfter, "with --session-profile: how many model calls old an unreferenced tool result has to be before it counts as stale")
	fs.StringVar(&o.decisionsReplay, "decisions-replay", "", "calibrate the optional verify-triage decisions model against a labeled state corpus (one JSON per line: name, truth, state) and exit")
	fs.StringVar(&o.answerID, "answer", "", "answer a --bg run waiting for permission or with an open question: --answer <id> shows it, --answer <id> allow|session|deny settles a permission request, --answer <id> <choice> answers a question (an option number, its label, or your own words), then exit")
	fs.StringVar(&o.backupTo, "backup", "", "write the config and data directories (config, permission rules, MCP logins, sessions, memory) to one tar.gz readable only by you, then exit")
	fs.StringVar(&o.restoreFrom, "restore", "", "put a --backup archive back, then exit. The files it replaces are saved to a tar.gz beside it first, so a restore can be undone")
	fs.BoolVar(&o.runDoctor, "doctor", false, "check config, credentials, a live model call, directories, MCP servers, hooks, verification, permissions, skills, memory and language servers, then exit")
	fs.BoolVar(&o.runSessionRef, "session-ref", false, "keep sessions in the repository as git refs, then exit; positional arguments: save [id] stores a session (default the latest here) under refs/janetcode/sessions/<id>, under 1 MB, with its proof records and probes | list | show <id> | open <id> puts a saved session among the local ones so --show and --what-if read it | push [remote] | fetch [remote] [id] moves saved sessions and the probe memory, which are local until pushed | probes lists the probe memory")
	fs.BoolVar(&o.runProof, "proof", false, "print the proof note attached to a commit -- which session, turn and intent wrote each of its files, and what actually demonstrated the work -- then exit; positional argument is a commit (default HEAD), or push [remote] | fetch [remote] to move proof notes to/from a remote, since they are local only until pushed; verify [commit] checks the commit's signed evidence and re-runs the commands it records, exiting 1 when it does not hold; key prints this machine's evidence public key")
	fs.StringVar(&o.recipeName, "recipe", "", "replay a saved recipe (.janetcode/recipes/<name>.json, see /recipe save) as tool calls with no model call and no tokens, then exit; extra positional arguments are k=v parameter substitutions; a step that fails, or the project's verify gate once every step has succeeded, hands over to the model if a provider is configured, otherwise the run exits non-zero")
	fs.BoolVar(&o.screenReader, "screen-reader", false, "a line interface for a screen reader instead of the full-screen one: every line written once and never redrawn, no animation, colour or box drawing, every state said in words, permission asked as a typed y/a/r/n (JANETCODE_SCREEN_READER=1, or screen_reader in the config; ACCESSIBILITY_ENABLED=1 turns it on too)")
	fs.BoolVar(&o.plain, "plain", false, "draw the terminal interface in ASCII with no animation, for the Linux console, a font without box drawing, or a quieter screen (JANETCODE_PLAIN=1; on by itself when TERM=linux)")
	fs.StringVar(&o.themeFlag, "theme", "", "colour theme for this run: auto, dark, light, or the name of a builtin or user theme (--inspect themes lists them)")
	fs.BoolVar(&o.web, "web", false, "serve the session in a browser instead of the terminal")
	fs.StringVar(&o.webAddr, "web-addr", "127.0.0.1:8787", "listen address for --web. Loopback by default; anyone who can reach the port can approve commands in this directory")
	fs.BoolVar(&o.bg, "bg", false, "run the prompt in the background, print the session id, and return; reopen with --resume <id>. What its permission mode asks about waits for --answer; add --yolo to allow everything")
	fs.Var(&o.addDir, "add-dir", "additional local directory file tools may read and edit (repeatable)")
	fs.Var(&o.sets, "set", "set a config key for this run, e.g. --set escalate_after=1 --set decisions.triage=true (repeatable). The keys are the ones /config takes, including the model slots and the decisions block; nothing is written to the config file. --inspect config lists them")
	return fs, o
}

// headless reports a run with no terminal UI: a -p prompt, or a
// stream-json conversation on stdin, which needs no -p at all.
func (o *options) headless() bool {
	return o.promptText != "" || o.inFormat == string(headless.InputStreamJSON)
}
