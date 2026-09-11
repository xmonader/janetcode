package main

import (
	"context"
	"flag"
	"reflect"
	"strings"

	"github.com/xmonader/janetcode/internal/inspect"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
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
	"p":                    "scripting janetcode, or one question you do not want a terminal session for",
	"output-format":        "a script or a CI job that has to read the result rather than a human",
	"max-cost":             "an unattended run you do not want to discover afterwards was expensive",
	"max-turns":            "capping the model calls a session may make, the same cap for the TUI, headless and ACP",
	"compact-at":           "compacting earlier than the default, to test compaction or to keep a small model within its window",
	"until":                "the run is done when a command passes, not when the model says so",
	"watch":                "you want failures fixed as you save, without pressing enter",
	"provider":             "this run should go somewhere other than the configured backend",
	"model":                "one run on a different model without changing the config",
	"effort":               "an Anthropic model and a task worth more or less thinking",
	"yolo":                 "a sandbox, a container, or work you have already decided to trust",
	"readonly":             "you want answers about a repository with no chance of it being changed",
	"c":                    "carry on where the last session in this directory left off",
	"r":                    "reopen one session by id",
	"auto-edits":           "you trust the edits but still want to see every command",
	"eval":                 "measuring a change to the harness or to a prompt, not doing work",
	"eval-jobs":            "the eval set is large and the machine can take more of it at once",
	"eval-only":            "you are iterating on one failing task",
	"eval-json":            "you want the run's numbers kept for a later comparison",
	"replay":               "checking that a harness change did not alter behaviour, without buying tokens",
	"eval-report":          "reading numbers you already paid for",
	"eval-keep":            "a task failed and you want to look at the directory it failed in",
	"eval-runs":            "one run per task is a coin flip; a comparison wants three or more",
	"eval-compare":         "deciding whether a change actually helped",
	"eval-off":             "an ablation: what does this behaviour buy",
	"eval-label":           "telling two variants apart in a results file",
	"eval-train":           "the trajectory critic has runs to learn from",
	"eval-timeout":         "giving every harness in a benchmark the same wall budget",
	"eval-max-turns":       "bounding what one task may spend in model calls",
	"eval-no-tool-limit":   "comparing against harnesses that enforce no tool budget",
	"inspect":              "you want one part of the state printed and nothing else to happen",
	"worktree":             "two sessions on one repository, or a change you want as a branch to review",
	"acp":                  "an editor that launches janetcode as a subprocess",
	"eval-facts":           "measuring what compaction actually loses",
	"eval-facts-blind":     "measuring what the compacted context retains without the archive",
	"eval-compact-at":      "forcing compaction on tasks that would not otherwise reach it",
	"allow":                "one run that should not stop to ask about a known-safe call",
	"deny":                 "one run that must not be able to do a particular thing",
	"continue":             "carry on where the last session in this directory left off",
	"resume":               "reopen one session by id",
	"version":              "you are reporting a bug, or a script needs the build",
	"sessions":             "you want the ids before choosing one to resume",
	"init":                 "first run: a config file to edit rather than a blank one to invent",
	"mcp-auth":             "a remote MCP server needs an OAuth login once",
	"append-system-prompt": "an experiment that adds instructions, recorded so the A/B is traceable",
	"doctor":               "something is not working and you want to know which part",
}

// configWhen describes each key of the config file. The struct's json
// tags are the list; this is the prose. TestEveryConfigKeyIsCatalogued
// fails when a key has no entry.
var configWhen = map[string][2]string{
	"provider":            {"which backend to talk to", "you have more than one configured"},
	"model":               {"the model id used when nothing overrides it", "the default is not the one you want"},
	"max_tokens":          {"cap on one reply's output tokens", "answers are being cut off, or are longer than you want to pay for"},
	"effort":              {"Anthropic thinking effort: low to max", "the work is worth more thinking, or is not"},
	"thinking":            {"whether to ask for reasoning at all", "a model or a task where reasoning is not worth the latency"},
	"intent":              {"ask the model for a one-line reason on every tool call", "you want a readable narrative and will pay ~21% more context for it"},
	"lean_tools":          {"defer the search tools behind tool_search", "on by default; 23% less context on fan-out work"},
	"show_thinking":       {"render the model's reasoning in the terminal", "you want the first visible token sooner and do not read the reasoning"},
	"auto_approve":        {"approve every tool call without asking", "a sandbox or a container"},
	"max_iterations":      {"cap on model calls in one turn", "a turn that loops is a turn that spends"},
	"compact_at":          {"estimated token count that triggers compaction", "you want compaction earlier, later, or never (negative disables)"},
	"theme":               {"dark, light or auto", "the terminal's own detection gets it wrong"},
	"providers":           {"the backends by name, with base urls and keys", "anything other than the default provider"},
	"anthropic":           {"legacy single-provider section, folded into providers", "an old config file that still has one"},
	"openai":              {"legacy single-provider section, folded into providers", "an old config file that still has one"},
	"prices":              {"prices per million tokens by model name or prefix", "the provider does not report cost and janetcode does not know the model"},
	"models":              {"context window, output limit and price learned by /connect", "you want the gauge and the cost to be right for a model nobody lists"},
	"routes":              {"send a model name, or a prefix, to the provider that serves it", "you want to ask for a model by name alone"},
	"mcp_servers":         {"MCP servers started for every project", "a tool server you use everywhere"},
	"hooks":               {"commands run around tool calls and turns", "you want a formatter, a guard or a notifier in the loop"},
	"permissions":         {"persistent allow and deny rules", "the same approval keeps coming up, or something must never run"},
	"embedding_model":     {"model asked of /embeddings so memory recall ranks by meaning", "word overlap is not finding the memory you meant"},
	"lsp":                 {"language servers consulted for diagnostics after edits", "you want the compiler's opinion before the tests'"},
	"search":              {"the web_search backend: DuckDuckGo, or Brave with a key", "you want better results than the free backend gives"},
	"fallback_models":     {"models tried, in order, when a call fails in a way another model could survive", "a quota that runs out mid-run should not end the run"},
	"stream_idle_timeout": {"seconds a streamed call may produce nothing before it is given up on", "a provider that stalls rather than failing"},
	"stream_max_duration": {"longest one streamed call may take", "a call that streams steadily for ever"},
	"memory":              {"extract project memory automatically after turns that changed something", "you would rather decide yourself what is remembered (/remember always works)"},
	"hints":               {"one-time hints about features you have not used", "you know the harness and want the transcript quiet"},
}

// harnessFeatures are the things janetcode does without being asked.
// They are in the catalogue because the commonest question a user has —
// and the commonest thing a model duplicates by hand — is about work
// the harness has already done.
var harnessFeatures = []feature.Feature{
	{Name: "undo checkpoints", Kind: feature.KindBehave, Invoke: "/undo", Manual: "interface",
		What:   "every turn that changes files is checkpointed first, in git where there is a repository and by file copy where there is not",
		When:   "you never have to ask for a backup before letting the model edit",
		Topics: []string{"backup", "revert", "safety", "checkpoint", "snapshot"}},
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
}

// buildCatalog assembles the catalogue for this build. It reads tables
// that are already in memory and allocates a few hundred small structs;
// it does no I/O and is not on the render path.
func buildCatalog(fs *flag.FlagSet, tools *tool.Registry) *feature.Catalog {
	c := feature.New()
	c.AddAll(tui.Features())
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			c.Add(feature.Feature{
				Name: f.Name, Kind: feature.KindFlag,
				What: firstSentence(f.Usage), When: flagWhen[f.Name],
				Invoke: "janetcode -" + f.Name, Manual: "cli",
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
func configKeys() []string {
	t := reflect.TypeOf(config.Config{})
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

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
				return c.Summary() + "\n" + c.List(), nil
			}
			return feature.Answer(c, arg, ""), nil
		},
	})
}
