package main

import (
	"flag"
	"fmt"
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
	maxCost      float64
	maxTurns     int
	compactAt    int
	until        string
	watchCmd     string
	provider     string
	model        string
	effort       string
	yolo         bool
	autoMode     bool
	sandboxOn    bool
	readonly     bool
	continueC    bool
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
	evalGate     bool
	inspectWhat  string
	wtBranch     string
	acpMode      bool
	evalFacts    int
	evalBlind    bool
	evalCompact  int
	allow        string
	deny         string
	cont         bool
	resume       string
	showVersion  bool
	listSess     bool
	initProject  bool
	initCfg      bool
	mcpAuth      string
	appendSystem string
	runDoctor    bool
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
	{"Session", []string{"model", "provider", "effort", "c", "continue", "r", "resume", "sessions", "web", "web-addr", "worktree", "add-dir", "theme"}},
	{"Headless (-p)", []string{"p", "output-format", "bg", "max-turns", "max-cost", "until", "watch"}},
	{"Permissions", []string{"auto", "auto-edits", "readonly", "yolo", "allow", "deny", "sandbox"}},
	{"Setup and diagnostics", []string{"init", "init-project", "doctor", "fix", "mcp-auth", "version"}},
	{"Advanced", nil},
	{"Eval and benchmark (see docs/manual/evals.md)", nil},
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

Usage: janetcode [flags] [prompt]   interactive session in the current directory,
                                     optionally starting with a prompt
       janetcode -p "prompt" [flags] run one prompt headless and exit

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
Environment: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL
             (a key may be a comma-separated list; they are rotated on rate limits),
             JANETCODE_PROVIDER, JANETCODE_MODEL, JANETCODE_EFFORT, JANETCODE_THEME
             JANETCODE_OFF=<names> disables behaviours for a session (see --eval-off)
             JANETCODE_SYSTEM_MD=<file> replaces the base system prompt
             JANETCODE_DEBUG=<file> appends a timestamped event log
Config file: $XDG_CONFIG_HOME/janetcode/config.json (default ~/.config/janetcode/config.json)
`)
	}
	o := &options{}
	fs.StringVar(&o.promptText, "p", "", "run this prompt headless and exit")
	fs.StringVar(&o.outFormat, "output-format", "text", "with -p: how the run reports itself. text prints the answer on stdout and activity on stderr; json prints one result object on stdout (answer, usage, cost and where the figure came from, turns, tool calls, permission denials, verdict, session id, duration); stream-json prints one JSON object per line as things happen, ending with that result")
	fs.Float64Var(&o.maxCost, "max-cost", 0, "with -p: stop once the run has spent this much in USD (exit 55), checked after every model call. The figure is the provider's when it reports one, otherwise list price")
	fs.IntVar(&o.maxTurns, "max-turns", 0, "cap the model calls one turn may make (0: the configured max_iterations). The same cap --eval-max-turns applies to an eval run, so a benchmark can give a headless run and an eval run the same budget")
	fs.IntVar(&o.compactAt, "compact-at", 0, "compact the history at this many estimated tokens instead of the configured value; the same knob --eval-compact-at applies to an eval run")
	fs.StringVar(&o.until, "until", "", "with -p: shell command that must exit 0 before the run is done (re-loops up to 5 times)")
	fs.StringVar(&o.watchCmd, "watch", "", "watch mode: re-run this shell command whenever files change and have the model fix failures (use with --yolo)")
	fs.StringVar(&o.provider, "provider", "", "configured provider or preset name, e.g. anthropic, openai, openrouter, ollama")
	fs.StringVar(&o.model, "model", "", "model id (default claude-opus-5 for anthropic)")
	fs.StringVar(&o.effort, "effort", "", "anthropic effort: low, medium, high, xhigh, max")
	fs.BoolVar(&o.yolo, "yolo", false, "auto-approve every tool call")
	fs.BoolVar(&o.autoMode, "auto", false, "auto mode: in-workspace edits and known-safe reads run; other commands ask (/auto toggles it)")
	fs.BoolVar(&o.sandboxOn, "sandbox", false, "wrap local bash in the workspace sandbox profile (docker, seatbelt or Landlock); the sandbox config key picks another profile; file tools stay on the host")
	fs.BoolVar(&o.readonly, "readonly", false, "read-only: no edits or commands, the model can only inspect")
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
	fs.StringVar(&o.inspectWhat, "inspect", "", "print one part of the session state and exit: context, events, archive, retrieval, compaction, tools, config, lsp, mcp, jobs, skills, repomap and more; `list` names them all")
	fs.StringVar(&o.wtBranch, "worktree", "", "run the session in a git worktree on this branch, in a checkout under .git: the real tree is untouched, two sessions can work on one repository at once, and the result is a branch to review and merge")
	fs.BoolVar(&o.acpMode, "acp", false, "serve the Agent Client Protocol on stdin/stdout, for an ACP editor that launches janetcode as a subprocess (nothing but protocol messages goes to stdout)")
	fs.IntVar(&o.evalFacts, "eval-facts", 0, "after each run that compacted, probe up to N facts mined from what compaction removed (exit codes, output lines, file lines) and record how many the model still recalls")
	fs.BoolVar(&o.evalBlind, "eval-facts-blind", false, "probe without expand_output: what the compacted context alone retains")
	fs.IntVar(&o.evalCompact, "eval-compact-at", 0, "compact the history at this many estimated tokens instead of the default: a low value forces compaction on tasks that would not otherwise reach it, to measure what it costs and what it loses")
	fs.StringVar(&o.allow, "allow", "", "comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'")
	fs.StringVar(&o.deny, "deny", "", "comma-separated permission rules to refuse for this run")
	fs.BoolVar(&o.cont, "continue", false, "resume the latest session for this directory")
	fs.StringVar(&o.resume, "resume", "", "resume the session with this id")
	fs.BoolVar(&o.showVersion, "version", false, "print version and exit")
	fs.BoolVar(&o.listSess, "sessions", false, "list saved sessions for this directory and exit")
	fs.BoolVar(&o.initCfg, "init", false, "write an example config file and exit")
	fs.BoolVar(&o.initProject, "init-project", false, "write a starter instruction file (the one read at the start of every session here) from what this repository shows -- languages, the commands it declares, its layout -- and exit; an existing one is never overwritten")
	fs.StringVar(&o.mcpAuth, "mcp-auth", "", "log in to the named remote MCP server with OAuth (browser flow) and exit")
	fs.StringVar(&o.appendSystem, "append-system-prompt", "", "text (or @file) appended to the system prompt; JANETCODE_SYSTEM_MD=<file> replaces the base prompt.")
	fs.BoolVar(&o.doctorFix, "fix", false, "with --doctor: carry out the safe repairs it lists (a misspelled config key, a missing config directory, an untrusted .janetcode/verify.json, an expired MCP login that cannot refresh, a directory of ours the owner cannot read) and print each change; anything needing judgement is still only reported")
	fs.StringVar(&o.decisionsAnnotate, "decisions-annotate", "", "label recorded sessions offline with the decisions model (a session .jsonl or a directory of them) and exit")
	fs.StringVar(&o.decisionsAnnotateOut, "decisions-annotate-out", "", "directory for --decisions-annotate's annotations.jsonl and critic-train.json (default: the working directory)")
	fs.StringVar(&o.sessionProfile, "session-profile", "", "classify the calls of recorded runs offline (an --eval-json result file, a session .jsonl, or a directory of either) as explore/edit/verify/other, report input-token growth and the share of context that is stale tool output, and exit")
	fs.IntVar(&o.sessionProfileStale, "session-profile-stale", profileStaleAfter, "with --session-profile: how many model calls old an unreferenced tool result has to be before it counts as stale")
	fs.StringVar(&o.decisionsReplay, "decisions-replay", "", "calibrate the optional verify-triage decisions model against a labeled state corpus (one JSON per line: name, truth, state) and exit")
	fs.BoolVar(&o.runDoctor, "doctor", false, "check config, credentials, a live model call, directories, MCP servers, hooks, verification, permissions, skills, memory and language servers, then exit")
	fs.BoolVar(&o.runProof, "proof", false, "print the proof note attached to a commit -- which session, turn and intent wrote each of its files, and what actually demonstrated the work -- then exit; positional argument is a commit (default HEAD), or push [remote] | fetch [remote] to move proof notes to/from a remote, since they are local only until pushed")
	fs.StringVar(&o.recipeName, "recipe", "", "replay a saved recipe (.janetcode/recipes/<name>.json, see /recipe save) as tool calls with no model call and no tokens, then exit; extra positional arguments are k=v parameter substitutions; a step that fails, or the project's verify gate once every step has succeeded, hands over to the model if a provider is configured, otherwise the run exits non-zero")
	fs.StringVar(&o.themeFlag, "theme", "", "colour theme for this run: auto, dark, light, or the name of a builtin or user theme (--inspect themes lists them)")
	fs.BoolVar(&o.web, "web", false, "serve the session in a browser instead of the terminal")
	fs.StringVar(&o.webAddr, "web-addr", "127.0.0.1:8787", "listen address for --web. Loopback by default; anyone who can reach the port can approve commands in this directory")
	fs.BoolVar(&o.bg, "bg", false, "run the prompt in the background, print the session id, and return; reopen with --resume <id>")
	fs.Var(&o.addDir, "add-dir", "additional local directory file tools may read and edit (repeatable)")
	fs.Var(&o.sets, "set", "set a config key for this run, e.g. --set escalate_after=1 --set decisions.triage=true (repeatable). The keys are the ones /config takes, including the model slots and the decisions block; nothing is written to the config file. --inspect config lists them")
	return fs, o
}
