package settingsapply

import (
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
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
		{"model", "some-other-model",
			func(a *agent.Agent) { a.Model = "starting-model" },
			func(a *agent.Agent) any { return a.Model },
			func(cfg *config.Config) any { return cfg.Model }},
		{"proof_notes", "false",
			func(a *agent.Agent) { a.ProofNotes = true },
			func(a *agent.Agent) any { return a.ProofNotes },
			func(cfg *config.Config) any { return cfg.ProofNotesEnabled() }},
		{"route_cheap", "haiku",
			func(a *agent.Agent) { a.RouteCheap = "starting-model" },
			func(a *agent.Agent) any { return a.RouteCheap },
			func(cfg *config.Config) any { return cfg.RouteCheap }},
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
		"effort":        "read from cfg.Effort when building each provider request",
		"theme":         "read from cfg.Theme by the front-end's theme picker",
		"hints":         "read via cfg.ShowHints() by the hint engine",
		"memory":        "read via cfg.AutoMemory() where memory extraction is scheduled",
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
