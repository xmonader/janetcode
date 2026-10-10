package settingsapply

import (
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestApplyUpdatesLiveAgent audits every key in config.SettableKeys
// against settingsapply.Apply's live-update switch (see settingsapply.go):
// /config setting a key must take effect in the running session, not just
// on the next one. cases lists the keys that have a live Agent field to
// update and checks it actually changed; noAgentField documents, by name,
// every other settable key and why it needs no case (its setting is read
// straight from *config.Config at the point of use instead of being
// cached on Agent). A key that is in neither list fails the test, so a
// newly added settable key cannot silently ship without this audit.
func TestApplyUpdatesLiveAgent(t *testing.T) {
	type tc struct {
		key    string
		val    string
		before func(a *agent.Agent) // seeds the field to a value neither zero nor val, so a no-op case can't pass by accident
		field  func(a *agent.Agent) any
		want   func(cfg *config.Config) any
	}
	cases := []tc{
		{"intent", "true",
			func(a *agent.Agent) { a.Tools.Intent = false },
			func(a *agent.Agent) any { return a.Tools.Intent },
			func(cfg *config.Config) any { return cfg.IntentEnabled() }},
		{"auto_approve", "true",
			func(a *agent.Agent) { a.AutoApprove = false },
			func(a *agent.Agent) any { return a.AutoApprove },
			func(cfg *config.Config) any { return cfg.AutoApprove }},
		{"compact_at", "999",
			func(a *agent.Agent) { a.CompactAt = 1 },
			func(a *agent.Agent) any { return a.CompactAt },
			func(cfg *config.Config) any { return cfg.CompactAt }},
		{"max_tokens", "555",
			func(a *agent.Agent) { a.MaxTokens = 1 },
			func(a *agent.Agent) any { return a.MaxTokens },
			func(cfg *config.Config) any { return cfg.MaxTokens }},
		{"max_iterations", "7",
			func(a *agent.Agent) { a.MaxIterations = 1 },
			func(a *agent.Agent) any { return a.MaxIterations },
			func(cfg *config.Config) any { return cfg.MaxIterations }},
		{"effort", "xhigh",
			func(a *agent.Agent) { a.Effort = "low" },
			func(a *agent.Agent) any { return a.Effort },
			func(cfg *config.Config) any { return cfg.Effort }},
		{"model", "some-other-model",
			func(a *agent.Agent) { a.Model = "starting-model" },
			func(a *agent.Agent) any { return a.Model },
			func(cfg *config.Config) any { return cfg.Model }},
		{"proof_notes", "false",
			func(a *agent.Agent) { a.ProofNotes = true },
			func(a *agent.Agent) any { return a.ProofNotes },
			func(cfg *config.Config) any { return cfg.ProofNotesEnabled() }},
		{"sandbox", "strict",
			func(a *agent.Agent) { a.Sandbox = &shell.Sandbox{}; _ = a.Sandbox.SetProfile("read-only") },
			func(a *agent.Agent) any { return a.Sandbox.ProfileName() },
			func(cfg *config.Config) any { return cfg.Sandbox }},
		{"route_cheap", "haiku",
			func(a *agent.Agent) { a.RouteCheap = "starting-model" },
			func(a *agent.Agent) any { return a.RouteCheap },
			func(cfg *config.Config) any { return cfg.RouteCheap }},
		{"advisor_model", "strong-model",
			func(a *agent.Agent) { a.AdvisorModel = "starting-model" },
			func(a *agent.Agent) any { return a.AdvisorModel },
			func(cfg *config.Config) any { return cfg.AdvisorModel }},
		{"strong_model", "escalate-here",
			func(a *agent.Agent) { a.StrongModel = "starting-model" },
			func(a *agent.Agent) any { return a.StrongModel },
			func(cfg *config.Config) any { return cfg.StrongModel }},
		{"stale_check_nudge", "true",
			func(a *agent.Agent) { a.StaleCheckNudge = false },
			func(a *agent.Agent) any { return a.StaleNudgeOn() },
			func(cfg *config.Config) any { return cfg.StaleCheckNudge }},
		{"effort_control", "true",
			func(a *agent.Agent) { a.EffortControl = false },
			func(a *agent.Agent) any { return a.EffortControlOn() },
			func(cfg *config.Config) any { return cfg.EffortControl }},
		{"elide_checks", "true",
			func(a *agent.Agent) { a.ElideChecks = false },
			func(a *agent.Agent) any { return a.ElideChecksOn() },
			func(cfg *config.Config) any { return cfg.ElideChecks }},
		{"localize_failures", "true",
			func(a *agent.Agent) { a.LocalizeFailures = false },
			func(a *agent.Agent) any { return a.LocalizeOn() },
			func(cfg *config.Config) any { return cfg.LocalizeFailures }},
		{"subagent_merge", "true",
			func(a *agent.Agent) { a.SubagentMerge = false },
			func(a *agent.Agent) any { return a.SubagentMergeOn() },
			func(cfg *config.Config) any { return cfg.SubagentMerge }},
		{"speculate", "true",
			func(a *agent.Agent) { a.Speculate = false },
			func(a *agent.Agent) any { return a.SpeculateOn() },
			func(cfg *config.Config) any { return cfg.Speculate }},
		{"plan_file", "true",
			func(a *agent.Agent) { a.PlanFile = false },
			func(a *agent.Agent) any { return a.PlanFileOn() },
			func(cfg *config.Config) any { return cfg.PlanFile }},
		{"scope_justify", "true",
			func(a *agent.Agent) { a.ScopeJustify = false },
			func(a *agent.Agent) any { return a.ScopeJustifyOn() },
			func(cfg *config.Config) any { return cfg.ScopeJustify }},
		{"probe_ledger", "true",
			func(a *agent.Agent) { a.ProbeLedger = false },
			func(a *agent.Agent) any { return a.ProbeLedgerOn() },
			func(cfg *config.Config) any { return cfg.ProbeLedger }},
		{"spec_coverage", "true",
			func(a *agent.Agent) { a.SetSpecCoverage(false) },
			func(a *agent.Agent) any { return a.SpecCoverageOn() },
			func(cfg *config.Config) any { return cfg.SpecCoverage }},
		{"spec_examples", "true",
			func(a *agent.Agent) { a.SetSpecExamples(false) },
			func(a *agent.Agent) any { return a.SpecExamplesOn() },
			func(cfg *config.Config) any { return cfg.SpecExamples }},
		{"flake_quarantine", "true",
			func(a *agent.Agent) { a.FlakeQuarantine = false },
			func(a *agent.Agent) any { return a.FlakeQuarantineOn() },
			func(cfg *config.Config) any { return cfg.FlakeQuarantine }},
		{"fix_cache", "true",
			func(a *agent.Agent) { a.FixCache, a.FixCacheDir = false, "/x" },
			func(a *agent.Agent) any { return a.FixCacheOn() },
			func(cfg *config.Config) any { return cfg.FixCache }},
		{"probe_memory", "true",
			func(a *agent.Agent) { a.ProbeMemory = false },
			func(a *agent.Agent) any { return a.ProbeMemoryOn() },
			func(cfg *config.Config) any { return cfg.ProbeMemory }},
		{"read_blame", "true",
			func(a *agent.Agent) { a.ReadBlame = false },
			func(a *agent.Agent) any { return a.ReadBlameOn() },
			func(cfg *config.Config) any { return cfg.ReadBlame }},
		{"numeric_claims_nudge", "true",
			func(a *agent.Agent) { a.NumericClaimsNudge = false },
			func(a *agent.Agent) any { return a.NumericClaimsNudgeOn() },
			func(cfg *config.Config) any { return cfg.NumericClaimsNudge }},
		{"diff_fuzz", "15",
			func(a *agent.Agent) { a.DiffFuzz = 0 },
			func(a *agent.Agent) any { return int(a.DiffFuzzBudget().Seconds()) },
			func(cfg *config.Config) any { return cfg.DiffFuzz }},
		{"observed_purity", "true",
			func(a *agent.Agent) { a.ObservedPurity = false },
			func(a *agent.Agent) any { return a.ObservedPurity },
			func(cfg *config.Config) any { return cfg.ObservedPurity }},
		{"ask_timeout", "90s",
			func(a *agent.Agent) { a.SetAskWait(agent.AskWaitForever, time.Minute) },
			func(a *agent.Agent) any { d, _ := a.AskWaitFor(); return d },
			func(cfg *config.Config) any { d, _, _ := cfg.AskWaits(); return d }},
		{"ask_timeout_unattended", "0",
			func(a *agent.Agent) { a.Unattended = true; a.SetAskWait(agent.AskWaitForever, time.Minute) },
			func(a *agent.Agent) any { d, _ := a.AskWaitFor(); return d },
			func(cfg *config.Config) any { _, d, _ := cfg.AskWaits(); return d }},
		{"escalate_after", "3",
			func(a *agent.Agent) { a.EscalateAfter = 1 },
			func(a *agent.Agent) any { return a.EscalateRounds() },
			func(cfg *config.Config) any { return cfg.EscalateAfter }},
		{"advisor_mode", "always",
			func(a *agent.Agent) { a.AdvisorMode = "off" },
			func(a *agent.Agent) any { return a.AdvisorMode },
			func(cfg *config.Config) any { return cfg.AdvisorMode }},
		{"advisor_turn_start_style", "orient",
			func(a *agent.Agent) { a.TurnStartStyle = "plan" },
			func(a *agent.Agent) any { return a.TurnStartStyle },
			func(cfg *config.Config) any { return cfg.AdvisorTurnStartStyle }},
		{"verify_baseline", "true",
			func(a *agent.Agent) { a.VerifyBaseline = false },
			func(a *agent.Agent) any { return a.VerifyBaseline },
			func(cfg *config.Config) any { return cfg.VerifyBaselineEnabled() }},
		{"language", "Spanish",
			func(a *agent.Agent) { a.SetLanguage("") },
			func(a *agent.Agent) any { return a.Language() },
			func(cfg *config.Config) any { return cfg.Language }},
		{"advisor_max_consults", "4",
			func(a *agent.Agent) { a.AdvisorMaxConsults = 1 },
			func(a *agent.Agent) any { return a.AdvisorMaxConsults },
			func(cfg *config.Config) any { return cfg.AdvisorMaxConsults }},
	}
	// noAgentField are settable keys with nothing cached on Agent for
	// Apply to update: each is read straight from *config.Config
	// wherever it is used (building a provider request, the hint engine,
	// the theme picker, lean-tools gating, ...), so there is no live field
	// that could go stale. If one of these ever gains an Agent-side cache,
	// move it into cases above instead of removing it from here.
	noAgentField := map[string]string{
		"thinking":      "read via cfg.ThinkingEnabled() when building each provider request",
		"show_thinking": "read via cfg.ThinkingShown() when building each provider request",
		"lean_tools":    "read via cfg.LeanTools() when tools are built for a request",
		"theme":         "read from cfg.Theme by the front-end's theme picker",
		"preresolve":    "read from cfg.Preresolve when the session's turn preparation is wired: it acts on a first message only",
		"prefix_kernel": "read from cfg.PrefixKernel when the session's system prompt is built: changing it mid-session would break the cached prefix it exists for",
		"hints":         "read via cfg.ShowHints() by the hint engine",
		"memory":        "read via cfg.AutoMemory() where memory extraction is scheduled",
		"followup":      "read via cfg.FollowupEnabled() by the terminal after a turn",
		"vim_mode":      "a terminal-only composer setting: the TUI reads cfg.VimMode at start and its /config applies it to the window",
		"computer_use":  "read via cfg.ComputerUseEnabled() when MCP servers start",
		"vision_model":  "no consumer yet: resolved through cfg.Slot wherever it is eventually read",
		"screen_reader": "read via cfg.ScreenReaderOn() when the session picks its interface",
		"conflict_exit": "read from cfg.ConflictExit when the agent is built: it adds a system prompt line, so it takes effect at the next session and never rewrites a cached prefix mid-session",
		"blind_tester":  "read via cfg.BlindTesterModel() when a headless run or an eval arm is built; an interactive session has no blind tester to update",
	}
	// The route mechanisms are read when the provider and agent are built.
	for _, k := range []string{"hedge_provider", "hedge_after_ms", "stream_resume", "fingerprint"} {
		noAgentField[k] = "read when the provider (wrapRoute) or agent (FingerprintDir) is built: takes effect at the next session"
	}
	for _, k := range config.SettableKeys {
		if strings.HasPrefix(k, "voice.") {
			noAgentField[k] = "read from cfg.Voice each time /voice transcribes"
		}
	}
	// The decisions block is wired as a whole rather than key by key
	// (see Apply), so its keys are audited by TestDecisionsKeysRewire
	// below instead of by a per-key live field.
	for _, k := range config.SettableKeys {
		if strings.HasPrefix(k, "decisions.") {
			noAgentField[k] = "wired as a block by WireDecisions"
		}
	}

	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.key] = true
	}
	for _, key := range config.SettableKeys {
		if seen[key] {
			continue
		}
		if _, ok := noAgentField[key]; ok {
			continue
		}
		t.Errorf("config.SettableKeys has %q, which is covered by neither this test's cases (a live Agent field) "+
			"nor noAgentField (read straight from config) -- audit settingsapply.Apply for a missing case", key)
	}
	// Every case above also has to be a real settable key, or the test
	// could pass on a key ApplyKV itself would reject.
	settable := map[string]bool{}
	for _, k := range config.SettableKeys {
		settable[k] = true
	}
	for _, c := range cases {
		if !settable[c.key] {
			t.Errorf("case %q is not in config.SettableKeys", c.key)
		}
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			cfg := &config.Config{}
			a := &agent.Agent{Tools: tool.NewRegistry()}
			c.before(a)
			if _, err := Apply(cfg, a, "", c.key, c.val); err != nil {
				t.Fatalf("Apply(%q, %q): %v", c.key, c.val, err)
			}
			want := c.want(cfg)
			if got := c.field(a); got != want {
				t.Errorf("after /config %s=%s, live agent field = %v, want %v", c.key, c.val, got, want)
			}
		})
	}
}

// /config model switches the model the way /model does: the context
// window and the price follow it. Setting only the name left a session
// moved to a 200k model compacting against the 1M window it came from,
// and pricing its calls at the old model's rates.
func TestConfigModelSwitchesWindowAndPrice(t *testing.T) {
	cfg := &config.Config{Models: map[string]config.ModelMeta{
		"big":   {ContextWindow: 1_000_000},
		"small": {ContextWindow: 200_000, Price: &llm.Price{Input: 1, Output: 2}},
	}}
	a := &agent.Agent{Tools: tool.NewRegistry()}
	a.SwitchModel("big", nil, cfg.PriceFor("big"), 1_000_000)
	if _, err := Apply(cfg, a, "", "model", "small"); err != nil {
		t.Fatal(err)
	}
	if a.Model != "small" || a.ContextWindow != 200_000 || a.Price == nil || a.Price.Input != 1 {
		t.Fatalf("after /config model=small: model %q window %d price %+v", a.Model, a.ContextWindow, a.Price)
	}
}

// Every decisions key rewires the whole block on the live agent, because
// its keys are not independent: a consumer switched on while the master
// switch is off has to end up with no client, exactly as a restart would
// leave it. With no key in the environment there is no client either way,
// which is the case this test can check without one.
func TestDecisionsKeysRewireTheBlock(t *testing.T) {
	// The default key env, emptied: the machine running the test may
	// well have a real one, and this case is the session that has none.
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg := &config.Config{}
	a := &agent.Agent{Tools: tool.NewRegistry()}
	if _, err := Apply(cfg, a, "", "decisions.api_key_env", "JANETCODE_TEST_DECISIONS_KEY"); err == nil {
		t.Fatal("api_key_env is not settable; the test is out of date")
	}
	if _, err := Apply(cfg, a, "", "decisions.triage", "on"); err != nil {
		t.Fatal(err)
	}
	if !cfg.Decisions.Triage {
		t.Fatal("the key did not reach the config")
	}
	// No key in the environment: no client, so no consumer is on.
	if a.DecisionsClient() || a.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("a keyless session must have no client and no consumer on")
	}

	// With a key the client appears and the consumer follows the config.
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	if _, err := Apply(cfg, a, "", "decisions.intake", "on"); err != nil {
		t.Fatal(err)
	}
	if !a.DecisionsClient() || !a.DecisionsOn(agent.DecisionTriage) || !a.DecisionsOn(agent.DecisionIntake) {
		t.Fatal("the block did not reach the agent")
	}
	// And the master switch takes the client away without forgetting the
	// consumers in the file.
	if _, err := Apply(cfg, a, "", "decisions.enabled", "off"); err != nil {
		t.Fatal(err)
	}
	if a.DecisionsClient() || a.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("/jev off must leave no client and no consumer on")
	}
	if !cfg.Decisions.Triage || !cfg.Decisions.Intake {
		t.Fatal("switching off must not forget the consumer keys")
	}
	if _, err := Apply(cfg, a, "", "decisions.enabled", "on"); err != nil {
		t.Fatal(err)
	}
	if !a.DecisionsClient() || !a.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("/jev on must bring the client and the consumers back")
	}
}

// The kill switch beats the configuration: with Off naming decisions,
// no key can put a client on the agent.
func TestDecisionsKillSwitchBeatsTheKeys(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	cfg := &config.Config{}
	a := &agent.Agent{Tools: tool.NewRegistry(), Off: map[string]bool{"decisions": true}}
	if _, err := Apply(cfg, a, "", "decisions.triage", "on"); err != nil {
		t.Fatal(err)
	}
	if a.DecisionsClient() || a.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("JANETCODE_OFF=decisions was overridden by /jev")
	}
}
