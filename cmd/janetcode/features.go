package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/inspect"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/theme"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/tui"
)

// The feature catalogue is assembled here, from the flag set that was
// just parsed, the tool registry that was just built and the Config
// struct itself. Nothing is copied out by hand, so a flag that is added
// without a line in flagWhen fails a test rather than quietly going
// undocumented.

// flagWhen is the "when it helps" line for each command-line flag. The
// flag's own usage string says what it does; this says who wants it.
// TestEveryFlagIsCatalogued fails when a flag has no entry.
var flagWhen = map[string]string{
	"p":                      "scripting janetcode, or one question you do not want a terminal session for",
	"output-format":          "a script or a CI job that has to read the result rather than a human",
	"input-format":           "a program that holds one session open and sends it message after message, interrupting when it needs to",
	"json-schema":            "a script that needs fields it can read, not prose it has to parse",
	"list-models":            "choosing a --model, or checking a name before a script depends on it",
	"debug":                  "a run that misbehaves and you want the timeline of requests, tool calls and MCP traffic behind it",
	"safe-mode":              "a repository you have not vetted yet, or telling whether a problem is janetcode's or a hook, MCP server or skill you added",
	"core-tools":             "a narrow job (review, triage, docs) where a short tool list keeps the model on task and the prompt small",
	"exclude-tools":          "a tool you never want offered in this run, e.g. bash in a review job, without even a refused attempt",
	"max-wall-time":          "a run in CI or a cron job that must finish, one way or the other, before the job's own timeout kills it without a result",
	"max-tool-calls":         "an unattended run that should give up rather than keep poking at the repository",
	"max-cost":               "an unattended run you do not want to discover afterwards was expensive",
	"max-turns":              "capping the model calls a session may make, the same cap for the TUI, headless and ACP",
	"compact-at":             "compacting earlier than the default, to test compaction or to keep a small model within its window",
	"until":                  "the run is done when a command passes, not when the model says so",
	"branch-retry":           "an unattended run that would rather spend a second attempt than end stuck, without paying again for the exploring the first one did",
	"second-sample":          "an unattended run where a confident wrong answer costs more than a second attempt does",
	"watch":                  "you want failures fixed as you save, without pressing enter",
	"provider":               "this run should go somewhere other than the configured backend",
	"model":                  "one run on a different model without changing the config",
	"effort":                 "an Anthropic model and a task worth more or less thinking",
	"yolo":                   "a sandbox, a container, or work you have already decided to trust",
	"auto":                   "you trust in-workspace edits and read-only commands but still want to see the rest",
	"sandbox":                "you want bash isolated in a sandbox profile without retargeting file tools",
	"readonly":               "you want answers about a repository with no chance of it being changed",
	"dry-run":                "you want the whole change — every diff, every command it would run — laid out before any of it happens",
	"trust":                  "a headless run should use (or refuse) the project's own skills, hooks, MCP servers and recipes without asking",
	"c":                      "carry on where the last session in this directory left off",
	"r":                      "reopen one session by id",
	"auto-edits":             "you trust the edits but still want to see every command",
	"eval":                   "measuring a change to the harness or to a prompt, not doing work",
	"eval-jobs":              "the eval set is large and the machine can take more of it at once",
	"eval-only":              "you are iterating on one failing task",
	"eval-json":              "you want the run's numbers kept for a later comparison",
	"replay":                 "checking that a harness change did not alter behaviour, without buying tokens",
	"eval-report":            "reading numbers you already paid for",
	"eval-keep":              "a task failed and you want to look at the directory it failed in",
	"eval-runs":              "one run per task is a coin flip; a comparison wants three or more",
	"eval-compare":           "deciding whether a change actually helped",
	"eval-sequential":        "an A/B against a saved baseline that should stop paying for runs once the answer is clear",
	"eval-off":               "an ablation: what does this behaviour buy",
	"eval-route":             "measuring whether the experimental step router (route_cheap) is worth turning on",
	"eval-label":             "telling two variants apart in a results file",
	"eval-train":             "the trajectory critic has runs to learn from",
	"eval-timeout":           "giving every harness in a benchmark the same wall budget",
	"eval-max-turns":         "bounding what one task may spend in model calls",
	"eval-no-tool-limit":     "comparing against harnesses that enforce no tool budget",
	"eval-strict-provenance": "a gateway might answer one model name with another model, and a run it swapped should fail the eval rather than be left out",
	"eval-gate":              "measuring the verification gate itself, or what failure triage does to a run",
	"inspect":                "you want one part of the state printed and nothing else to happen",
	"worktree":               "two sessions on one repository, or a change you want as a branch to review",
	"acp":                    "an editor that launches janetcode as a subprocess",
	"mcp-serve":              "another agent or MCP client should be able to hand janetcode a task",
	"plugin":                 "installing or updating plugins from a script, a dotfiles setup or CI",
	"eval-facts":             "measuring what compaction actually loses",
	"eval-facts-blind":       "measuring what the compacted context retains without the archive",
	"eval-compact-at":        "forcing compaction on tasks that would not otherwise reach it",
	"allow":                  "one run that should not stop to ask about a known-safe call",
	"deny":                   "one run that must not be able to do a particular thing",
	"ask":                    "one run that must always stop and ask before a sensitive call, even under --yolo",
	"continue":               "carry on where the last session in this directory left off",
	"resume":                 "reopen one session by id",
	"version":                "you are reporting a bug, or a script needs the build",
	"update":                 "a new release is out and you installed janetcode from a release binary",
	"uninstall":              "you are done with janetcode and want the binary it installed gone",
	"purge":                  "an uninstall that should also take your settings, keys and sessions with it; --backup them first if there is any doubt",
	"sessions":               "you want the ids before choosing one to resume",
	"fork":                   "the session you want is open in another janetcode, or you want to branch it and keep the original",
	"recover":                "a background run or a scripted one died mid-turn and you want that turn carried on, not re-asked",
	"show":                   "reading a past conversation, or one another janetcode is running, without opening it",
	"search":                 "you remember what was said but not which session it was in",
	"audit":                  "you need to show what a session did, what allowed each change, and that its record has not been altered since",
	"init":                   "first run: a config file to edit rather than a blank one to invent",
	"mcp-auth":               "a remote MCP server needs an OAuth login once",
	"append-system-prompt":   "an experiment that adds instructions, recorded so the A/B is traceable",
	"doctor":                 "something is not working and you want to know which part",
	"answer":                 "a --bg run is waiting for your permission, or asked a question, and you want to settle it from another terminal or a phone",
	"backup":                 "you are moving to another machine, or want a copy of your settings and sessions before changing them",
	"restore":                "you are setting up a new machine from a backup, or undoing a change to your settings",
	"decisions-replay":       "measuring whether the optional verify-triage decisions model is calibrated before trusting it live",
	"decisions-annotate":     "labelling recorded sessions offline so the local trajectory critic has failing runs to learn from",
	"session-profile":        "finding out where a recorded run's calls and input tokens went before trying to cut either",
	"session-profile-stale":  "tuning how old an untouched tool result has to be before --session-profile calls it dead weight",
	"decisions-annotate-out": "putting an annotation run's two files somewhere other than the working directory",
	"init-project":           "a repository janetcode has never seen: it writes the instruction file every session here will read",
	"fix":                    "the doctor listed repairs you want carried out: the safe, obvious ones only",
	"proof":                  "code review, or months later: which session and turn wrote a committed file and what actually demonstrated it worked",
	"theme":                  "one run in a different colour scheme without changing the config",
	"screen-reader":          "you use a screen reader or a braille display, and a screen that redraws itself reads as noise",
	"plain":                  "the terminal shows boxes or question marks where lines and marks should be, or you want a screen that does not move",
	"web":                    "you want the session in a browser, on another screen or from a phone on the same machine",
	"web-addr":               "the default loopback port is taken, or you are deliberately binding off loopback",
	"bg":                     "a prompt you want to keep running after this terminal is gone; reopen with --resume",
	"add-dir":                "a sibling tree or generated directory the file tools should be allowed to touch this session",
	"set":                    "a run with nobody to type /config in it still needs a config key changed: a benchmark arm that turns the cascade or a decision consumer on, without editing the file every other run reads",
	"recipe":                 "a turn's tool calls worked and you want to rerun them from a script or CI with no model call and no tokens",
	"what-if":                "a fix landed and you want to know what a different model, a disabled behaviour or a different prompt would have done from that point on, without redoing the turns that already worked",
	"from-turn":              "--what-if needs to know which turn is the one being changed",
	"off":                    "with --what-if: an ablation for the live part of a counterfactual run",
	"prompt":                 "with --what-if: the turn being changed also needs different words, not just a different model",
	"what-if-keep":           "a counterfactual run's worktree is worth a look after the fact",
	"json":                   "a script wants --what-if's comparison as data, not prose",
}

// configWhen describes each key of the config file. The struct's json
// tags are the list; this is the prose. TestEveryConfigKeyIsCatalogued
// fails when a key has no entry.
var configWhen = map[string][2]string{
	"provider":                 {"which backend to talk to", "you have more than one configured"},
	"model":                    {"the model id used when nothing overrides it", "the default is not the one you want"},
	"max_tokens":               {"cap on one reply's output tokens", "answers are being cut off, or are longer than you want to pay for"},
	"effort":                   {"Anthropic thinking effort: low to max", "the work is worth more thinking, or is not"},
	"thinking":                 {"whether to ask for reasoning at all", "a model or a task where reasoning is not worth the latency"},
	"intent":                   {"ask the model for a one-line reason on every tool call", "you want a readable narrative and will pay ~21% more context for it"},
	"lean_tools":               {"defer the search tools behind tool_search", "on by default; 23% less context on fan-out work"},
	"show_thinking":            {"render the model's reasoning in the terminal", "you want the first visible token sooner and do not read the reasoning"},
	"auto_approve":             {"approve every tool call without asking", "a sandbox or a container"},
	"max_iterations":           {"cap on model calls in one turn", "a turn that loops is a turn that spends"},
	"ca_file":                  {"PEM file of extra certificate authorities every HTTPS connection trusts (JANETCODE_CA_FILE adds one from the environment)", "you are behind a TLS-intercepting proxy or use a gateway with a private CA"},
	"compact_at":               {"estimated token count that triggers compaction", "you want compaction earlier, later, or never (negative disables)"},
	"theme":                    {"auto, dark, light or a named palette", "the terminal's own detection gets it wrong, or you want one of the builtin or user themes"},
	"themes":                   {"user-defined palettes by name, each a role→colour map with an optional base", "you want a colour scheme the builtins do not ship, or to tweak one that does"},
	"providers":                {"the backends by name, with base urls and keys", "anything other than the default provider"},
	"anthropic":                {"legacy single-provider section, folded into providers", "an old config file that still has one"},
	"openai":                   {"legacy single-provider section, folded into providers", "an old config file that still has one"},
	"prices":                   {"prices per million tokens by model name or prefix", "the provider does not report cost and janetcode does not know the model"},
	"models":                   {"context window, output limit and price learned by /connect", "you want the gauge and the cost to be right for a model nobody lists"},
	"routes":                   {"send a model name, or a prefix, to the provider that serves it", "you want to ask for a model by name alone"},
	"mcp_servers":              {"MCP servers started for every project", "a tool server you use everywhere"},
	"hooks":                    {"commands run around tool calls and turns", "you want a formatter, a guard or a notifier in the loop"},
	"keybindings":              {"move the prompt's keys: action name to a key, a list of keys, or \"\" to unbind (\"effort\": \"f2\", \"external_editor\": \"ctrl+x ctrl+e\"); /help keys lists the actions", "a key clashes with your terminal or multiplexer, or your fingers expect another binding"},
	"vim_mode":                 {"start the composer in vim mode: esc for normal mode (hjkl, w/b/e, 0/$, x, dd, cw, ciw), i/a/I/A/o back to insert; /vim toggles it", "you edit prompts the way you edit code"},
	"status_line":              {"a command whose output is shown under the input", "you want the branch, the cost or anything a script can print always in view"},
	"statusLine":               {"status_line, under the name Claude Code and Qwen Code give it", "you are pasting a status line from their settings"},
	"permissions":              {"persistent allow and deny rules", "the same approval keeps coming up, or something must never run"},
	"memory_confirm":           {"hold what the model or the extractor saves to memory until you approve it with /memory pending", "you want to see every durable memory before it is kept"},
	"egress":                   {"which hosts a turn may send data to (default allow|ask|block, and an allow list); model requests are always allowed", "a repository or its data must not reach hosts you have not named"},
	"protected_paths":          {"extra paths a write always asks about, appended to the built-in set", "a file of yours has effects that outlive the session"},
	"embedding_model":          {"model asked of /embeddings so memory recall ranks by meaning", "word overlap is not finding the memory you meant"},
	"lsp":                      {"language servers consulted for diagnostics after edits", "you want the compiler's opinion before the tests'"},
	"search":                   {"the web_search backend: DuckDuckGo, or Brave with a key", "you want better results than the free backend gives"},
	"fallback_models":          {"models tried, in order, when a call fails in a way another model could survive", "a quota that runs out mid-run should not end the run"},
	"stream_idle_timeout":      {"seconds a streamed call may produce nothing before it is given up on", "a provider that stalls rather than failing"},
	"stream_max_duration":      {"longest one streamed call may take", "a call that streams steadily for ever"},
	"memory":                   {"extract project memory automatically after turns that changed something", "you would rather decide yourself what is remembered (/remember always works)"},
	"hints":                    {"one-time hints about features you have not used", "you know the harness and want the transcript quiet"},
	"update_check":             {"say in the TUI when a newer release is out, asking GitHub at most once a day", "you would rather the TUI made no network call of its own, or you update through a package manager"},
	"proof_notes":              {"attach evidence of this session's changes to a commit it makes, as a git note", "you review commits without the session that made them, or want the write skipped"},
	"sandbox":                  {"the sandbox profile a session in this project starts under: off, workspace, read-only or strict", "this project's commands should always be confined, not only when you remember to type /sandbox"},
	"localize_failures":        {"when a test run goes red after the turn changed several hunks, rerun the failing tests on subsets of those hunks in a throwaway worktree and attach the fewest that still fail them, plus the functions the failing tests ran ranked by Ochiai (Go tests for now)", "a model that re-reads and guesses after a multi-hunk edit breaks a test; off by default because it reruns tests, up to a minute, while the model waits"},
	"conflict_exit":            {"let the model end a turn with CONFLICT: <test> | <task quote> | <why> when a test contradicts the task; a confirmed one (the test exists and fails) ends the turn, and a -p run with exit 58", "tasks whose tests may be wrong, where a green won by special-casing is worse than a stop; off by default because it adds a prompt line"},
	"subagent_merge":           {"merge an editing subagent's work into the project when it finishes: edits that commute with what changed meanwhile are applied, read-set conflicts and overlaps go back to the subagent for one repair round", "you run several editing subagents in parallel on overlapping files; off by default because it writes to the project without a patch being applied by hand"},
	"stale_check_nudge":        {"send a turn back once when its checks passed, it edited again afterwards and it then said it was done", "a model that tests in the middle of a turn and edits at the end of it; off by default because it changes what a turn does"},
	"effort_control":           {"experimental: set each call's reasoning tier from the turn itself — low to start, a rung up after a failed edit, a red check or a loop, halved after progress, never above effort", "a reasoning model over-plans its first reply; it acts only where the provider sends a tier and its cache does not key on it (OpenAI Responses), so it is idle on Anthropic and OpenAI-compatible servers"},
	"elide_checks":             {"after a step whose last change was an edit nothing checked, run the project's check (verify commands, else the model's own last check under auto-approve) and give the model the result with that step's results; a repeat of that exact command on an unchanged tree is answered from it", "the model spends a round trip after every edit asking for the test run; off by default because it changes what the model sees"},
	"probe_ledger":             {"keep the inline checks the model runs after editing: each is run once against the tree as it was before the turn's first change, the ones that fail there and pass now are replayed at every later gate, and the spec review is told which ones passed on the old code too", "a model that checks rules the tests miss with inline asserts and then breaks one with a later edit; off by default because it reruns commands and can send a turn back"},
	"route_cheap":              {"experimental: send a turn's mechanical steps (reads, greps, checks that already passed) to this model instead", "an eval run showed it saves cost without moving the pass rate; empty is off"},
	"followup":                 {"cheap next-prompt ghost text after a turn; tab accepts", "you want a suggested next prompt in the input box without another full turn"},
	"computer_use":             {"start Cua Driver as an MCP server when cua-driver is on PATH", "you want the model to operate the desktop through accessibility, not pixel clicks"},
	"advisor_model":            {"model the deferred advisor tool consults when the executor is stuck; empty is the session model", "you want a stronger model to advise a cheap executor without putting advisor on every request"},
	"advisor_turn_start_style": {"what the consult at the start of a turn asks for: \"plan\" (the default; only with advisor_mode always) or \"orient\": a model reads the request, the checks and the likeliest files and says where to LOOK, what to WATCH FOR and how to CHECK, never what to change; orient runs every turn, on the advisor slot, else the strong slot, else the session's model", "you are measuring whether pointing a model at the code before it starts beats handing it a plan (docs/benchmarks/orient-baseline)"},
	"verify_baseline":          {"run the verify commands at turn start, before the first change, so the gate sends a turn back only for failures the turn caused; pre-existing ones are reported, not repaired in a loop", "the project's checks were already failing before you asked, and every turn spends repair rounds on failures it did not cause"},
	"advisor_mode":             {"whether the harness forces advisor consults (\"always\") or leaves the advisor as a tool the model may press (\"off\", the default)", "you are measuring whether a stronger model's written guidance raises a cheap executor's pass rate, which a tool the model declines to call cannot tell you"},
	"advisor_max_consults":     {"forced advisor consults one turn may buy; 0 is two", "a forced advisor is worth its price at one consult a turn but not at five"},
	"strong_model":             {"the strong slot of the cheap/default/strong trio: the model a turn escalates to when its verification gate keeps failing", "you want to run on a cheap model and buy the expensive one only for the turns that actually fail a real check"},
	"escalate_after":           {"verification rounds the session's own model may fail before the rest of the turn runs on strong_model; 0 is off", "you are measuring or using the cascade and want to say how patient it is"},
	"vision_model":             {"a model that reads images for a session model that cannot: each image is sent to it and its description goes to the session's model", "your model is text-only (DeepSeek, Qwen Coder, GLM) and you paste screenshots or use MCP tools that return images"},
	"language":                 {"the language the model answers in and the interface is shown in (Spanish and French are catalogued; others fall back to English for the interface)", "you would rather read and talk in a language other than English"},
	"screen_reader":            {"start every session in the screen-reader line interface (--screen-reader)", "you always use a screen reader, so the full-screen interface is never the one you want"},
	"voice":                    {"where /voice records with and sends recordings to: model, endpoint, api_key_env, recorder", "your provider has no transcription endpoint, or its speech model is not called whisper-1"},
	"decisions":                {"optional TypeSafe Jev decision client for advisory triage of verification failures; states are sent to its API, scrubbed of credential-shaped text", "a failing verify gate sends the model back for environmental or flaky failures it cannot fix; off unless configured"},
	"ask_timeout":              {"how long a question from the model waits for your answer in an interactive turn before the turn goes on with the model's recommended choice: 90s, 5m, 0, or forever (the default); a late answer still reaches the model", "you sometimes walk away mid-turn and would rather it carry on with a sensible default than wait for you"},
	"ask_timeout_unattended":   {"the same wait for a turn nobody is necessarily watching (a --bg run, a goal round, a /loop firing); default 2m, 0 goes on at once", "an unattended run should not stall on a question, or should give you longer to answer it from your phone"},
}

// harnessFeatures are the things janetcode does without being asked.
// They are in the catalogue because the commonest question a user has —
// and the commonest thing a model duplicates by hand — is about work
// the harness has already done.
var harnessFeatures = []feature.Feature{
	{Name: "undo checkpoints", Kind: feature.KindBehave, Invoke: "/undo", Manual: "interface",
		What:   "every turn that changes files is checkpointed first, in git where there is a repository and by file copy where there is not; /undo turn <n> goes back to any of them at once, rewinding the conversation with the files so the model stops believing it did the reverted work (--files-only keeps the conversation)",
		When:   "you never have to ask for a backup before letting the model edit, and a whole run of turns can be rolled back as one",
		Topics: []string{"backup", "revert", "safety", "checkpoint", "snapshot", "restore", "rollback", "turn", "rewind"}},
	{Name: "/routing", Kind: feature.KindCommand, Manual: "interface",
		What:   "toggle the step router for this session: while it is off, every model call goes to the session's own model no matter what route_cheap names",
		When:   "a route fires you do not trust, or a turn must stay on one model to the end",
		Topics: []string{"routing", "route", "route_cheap", "cheap", "model", "slot"}},
	{Name: "verify gate", Kind: feature.KindBehave, Invoke: "/verify set <cmd>", Manual: "interface",
		What:   "configured check commands run after a turn that changed code, and failures go back to the model to fix",
		When:   "you want the loop closed without watching it",
		Topics: []string{"tests", "build", "gate", "ci", "lint", "check"}},
	{Name: "memory extraction", Kind: feature.KindBehave, Invoke: "/memory", Manual: "interface",
		What:   "facts worth keeping are extracted after turns that changed something and come back in later sessions here",
		When:   "you are tired of re-explaining the project every morning",
		Topics: []string{"remember", "persist", "across", "sessions", "learn", "recall"}},
	{Name: "compaction", Kind: feature.KindBehave, Invoke: "/compact", Manual: "interface",
		What:   "a long conversation is pruned and summarised automatically, with the exact output kept in an archive that expand_output reads back",
		When:   "you do not have to start a new session because the context filled up",
		Topics: []string{"context", "window", "full", "long", "output", "summary", "archive"}},
	{Name: "background jobs", Kind: feature.KindBehave, Invoke: "ctrl+g, /jobs", Manual: "interface",
		What:   "a command can run in the background and be promoted out of the foreground mid-turn",
		When:   "a build or a test suite is holding the turn open",
		Topics: []string{"background", "slow", "long", "running", "promote"}},
	{Name: "permission rules", Kind: feature.KindBehave, Invoke: "/permissions, r at a prompt", Manual: "interface",
		What:   "approvals can be saved as rules by shape, so matching calls never ask again",
		When:   "the third time the same kind of call asks",
		Topics: []string{"approve", "allow", "deny", "rule", "trust", "prompt"}},
	{Name: "sessions", Kind: feature.KindBehave, Invoke: "/resume, -c", Manual: "sessions",
		What:   "every session is a JSONL log that can be listed, searched, resumed and forked",
		When:   "you closed the terminal and want the conversation back",
		Topics: []string{"resume", "continue", "history", "reopen", "fork"}},
	{Name: "goal loop", Kind: feature.KindBehave, Invoke: "/goal <objective>", Manual: "interface",
		What:   "the model keeps going round after round towards a stated goal until it is done or blocked",
		When:   "a job that will take many turns and you would rather not press enter for each one",
		Topics: []string{"autonomous", "loop", "rounds", "keep", "going"}},
	{Name: "local only", Kind: feature.KindBehave, Invoke: "/stats", Manual: "configuration",
		What:   "counts of which features you have used are kept in the config directory and never leave the machine",
		When:   "you want to know what janetcode records about you: this, and nothing sent anywhere",
		Topics: []string{"telemetry", "privacy", "tracking", "analytics", "usage"}},
	{Name: "flavor status", Kind: feature.KindBehave, Invoke: "(while the model is thinking)", Manual: "interface",
		What:   "the status line cycles through seeded Claude verbs and Qwen loading phrases while waiting on the model",
		When:   "the spinner would otherwise sit on the word thinking",
		Topics: []string{"spinner", "thinking", "flavor", "status", "fun", "moonwalking", "qwen"}},
	{Name: "advisor", Kind: feature.KindBehave, Invoke: "advisor (model tool)", Manual: "interface",
		What:   "a deferred consult the executor calls when stuck; the harness forwards the transcript; at most 3 uses per turn; never on the always-on schema list",
		When:   "a cheap executor is spinning and a stronger model has an information edge from the transcript",
		Topics: []string{"stuck", "advise", "consult", "oversight", "planner"}},
	{Name: "Cua Driver", Kind: feature.KindBehave, Invoke: "/cua on", Manual: "interface",
		What:   "Cua Driver (cua-driver or open-computer-use) is started as an MCP server so the model can observe accessibility trees and act on element tokens; screenshots from the driver are attached as images",
		When:   "you want desktop automation that is not raw xdotool clicks",
		Topics: []string{"computer", "desktop", "cua", "accessibility", "screenshot", "click", "driver", "mcp"}},
	{Name: "web trajectory", Kind: feature.KindBehave, Invoke: "Trajectory (web)", Manual: "interface",
		What:   "the --web conversation header switches Chat and Trajectory, the session event ledger with a timing strip and a record inspector",
		When:   "you want the raw turn/tool/compaction record without leaving the browser",
		Topics: []string{"trajectory", "ledger", "timeline", "web", "inspect"}},
}

// buildCatalog assembles the catalogue for this build. It reads tables
// that are already in memory and allocates a few hundred small structs;
// it does no I/O and is not on the render path.
func buildCatalog(fs *flag.FlagSet, tools *tool.Registry) *feature.Catalog {
	c := feature.New()
	c.AddAll(tui.Features())
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			// The spelling --help and the docs use: -p and -c, --resume.
			dash := "--"
			if len(f.Name) == 1 {
				dash = "-"
			}
			c.Add(feature.Feature{
				Name: f.Name, Kind: feature.KindFlag,
				What: firstSentence(f.Usage), When: flagWhen[f.Name],
				Invoke: "janetcode " + dash + f.Name, Manual: "cli",
			})
		})
	}
	if tools != nil {
		for _, n := range tools.Names() {
			t, ok := tools.Get(n)
			if !ok {
				continue
			}
			c.Add(feature.Feature{
				Name: n, Kind: feature.KindTool,
				What:   firstSentence(t.Description()),
				When:   "the model calls it; you do not",
				Invoke: n + " (model tool)", Manual: "interface",
			})
		}
	}
	for _, k := range configKeys() {
		w := configWhen[k]
		c.Add(feature.Feature{
			Name: k, Kind: feature.KindConfig,
			What: w[0], When: w[1],
			Invoke: `"` + k + `" in config.json`, Manual: "configuration",
		})
	}
	c.AddAll(harnessFeatures)
	return c
}

// configKeys lists the json keys of the Config struct, which is the only
// list of them that cannot be out of date.
func configKeys() []string { return config.Keys() }

// firstSentence trims a usage or description string to its first
// sentence, so a catalogue row stays one line.
func firstSentence(s string) string { return feature.Headline(s, 41) }

// untrustedInput says which of the project's own files are present but
// not enabled. A file sitting in the repository does nothing until it is
// trusted, and a user who does not know that thinks the feature is
// broken.
type untrustedInput struct {
	mcp, hooks, perms, verifying bool
}

func untrustedProjectFiles(in untrustedInput) []string {
	var out []string
	if in.mcp {
		out = append(out, ".mcp.json")
	}
	if in.hooks {
		out = append(out, ".janetcode/hooks.json")
	}
	if in.perms {
		out = append(out, ".janetcode/permissions.json")
	}
	if in.verifying {
		out = append(out, ".janetcode/verify.json")
	}
	return out
}

// unpricedModel names the model when it came from the environment and
// nothing can price it, which is why the cost figures will be blank.
// Only the environment, because a model written into the config file is
// a deliberate choice made next to the prices.
func unpricedModel(cfg *config.Config, getenv func(string) string) string {
	if cfg == nil || getenv("JANETCODE_MODEL") == "" || cfg.Model == "" {
		return ""
	}
	if cfg.PriceFor(cfg.Model) != nil {
		return ""
	}
	return cfg.Model
}

// outputWidth is how wide the catalogue listing may be: the terminal's
// width when there is a terminal, $COLUMNS when a pager or a script has
// set one, and eighty otherwise. Piped output gets eighty rather than
// one long line, because the thing on the other end of the pipe is
// usually less or a file somebody will read.
func outputWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w >= 40 {
		return w
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 40 {
		return n
	}
	return 80
}

// registerThemeSubject makes the colour themes answerable through
// --inspect themes and /inspect themes: the builtins and any user themes,
// each with its mode and description and the active one marked. It needs
// only the resolved theme set, so it works even when the run has no
// provider.
func registerThemeSubject(r *inspect.Registry, cfg *config.Config) {
	r.AddText("themes", "the colour themes available: builtins and user themes, with the active one marked", func() string {
		return themesReport(cfg)
	})
}

func themesReport(cfg *config.Config) string {
	set := cfg.ThemeSet
	if set == nil {
		if s, err := theme.Build(nil); err == nil {
			set = s
		}
	}
	if set == nil {
		return "no themes"
	}
	active := "auto"
	if cfg != nil && cfg.Theme != "" {
		active = cfg.Theme
	}
	var b strings.Builder
	fmt.Fprintf(&b, "active theme: %s\n\n", active)
	for _, t := range set.Sorted() {
		mark := "  "
		if t.Name == active {
			mark = "* "
		}
		mode := t.Mode()
		src := "builtin"
		if !t.Builtin {
			src = "user"
		}
		fmt.Fprintf(&b, "%s%-16s %-7s %-8s %s\n", mark, t.Name, mode, src, t.Desc)
	}
	b.WriteString("\nselect with the config \"theme\" key, --theme, or /theme in the session.")
	return b.String()
}

// registerFeatureSubject makes the catalogue answerable through
// --inspect features and /inspect features, beside every other part of
// the session state.
func registerFeatureSubject(r *inspect.Registry, c *feature.Catalog) {
	r.Add(inspect.Subject{
		Name:    "features",
		Summary: "everything janetcode can do: commands, keys, flags, tools, config keys",
		Usage:   "[query]",
		Report: func(_ context.Context, arg string) (string, error) {
			if strings.TrimSpace(arg) == "" {
				return c.Summary() + "\n" + c.ListWidth(outputWidth()), nil
			}
			return feature.Answer(c, arg, ""), nil
		},
	})
}
