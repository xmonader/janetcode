package config

import (
	"sort"
	"testing"
)

// SettableKeys is what /config advertises. It is derived from the table
// now, but the contract is what matters: nothing advertised that cannot
// be set, nothing settable that is not advertised, and a stable order so
// the listing does not shuffle between runs.
func TestSettableKeysMatchTheTable(t *testing.T) {
	if len(SettableKeys) != len(setters) {
		t.Fatalf("%d keys advertised, %d implemented", len(SettableKeys), len(setters))
	}
	for _, k := range SettableKeys {
		if _, ok := setters[k]; !ok {
			t.Errorf("%q is advertised but not implemented", k)
		}
	}
	if !sort.StringsAreSorted(SettableKeys) {
		t.Errorf("SettableKeys is not sorted: %v", SettableKeys)
	}
	for _, want := range []string{"model", "effort", "theme", "sandbox", "auto_approve", "max_tokens"} {
		if _, ok := setters[want]; !ok {
			t.Errorf("%q is no longer settable", want)
		}
	}
}

// Every catalogued key refuses an unusable value rather than storing it,
// and none of them panics on one.
func TestEverySettableKeyRefusesJunk(t *testing.T) {
	// Keys whose value is an opaque name this package cannot check: a
	// model id, a route, a theme. They are validated where they are used.
	free := map[string]bool{
		"route_cheap": true, "advisor_model": true, "strong_model": true,
		"vision_model": true, "theme": true, "model": true,
		"voice.model": true, "voice.recorder": true,
	}
	for _, k := range SettableKeys {
		if free[k] {
			continue
		}
		if _, err := ApplyKV(&Config{}, k, "!! not a value !!"); err == nil {
			t.Errorf("%s accepted junk", k)
		}
	}
	// model does refuse the one value it can judge.
	if _, err := ApplyKV(&Config{}, "model", ""); err == nil {
		t.Error("an empty model was accepted")
	}
	if _, err := ApplyKV(&Config{}, "no_such_key", "1"); err == nil {
		t.Error("an unknown key was accepted")
	}
	if _, err := ApplyKV(nil, "model", "m"); err == nil {
		t.Error("a nil config was accepted")
	}
}

// The two capped integers refuse zero and below: that is not a smaller
// budget, it is a session that cannot make a call.
func TestPositiveIntKeysRefuseZero(t *testing.T) {
	for _, k := range []string{"max_tokens", "max_iterations"} {
		for _, v := range []string{"0", "-1"} {
			if _, err := ApplyKV(&Config{}, k, v); err == nil {
				t.Errorf("%s accepted %s", k, v)
			}
		}
	}
	// compact_at is not capped that way: 0 means "use the default".
	if _, err := ApplyKV(&Config{}, "compact_at", "0"); err != nil {
		t.Errorf("compact_at=0: %v", err)
	}
}

func TestApplyKVThinking(t *testing.T) {
	cfg := &Config{}
	v, err := ApplyKV(cfg, "thinking", "false")
	if err != nil {
		t.Fatal(err)
	}
	if v != false || cfg.Thinking == nil || *cfg.Thinking {
		t.Fatalf("got %v cfg=%v", v, cfg.Thinking)
	}
	if _, err := ApplyKV(cfg, "nope", "1"); err == nil {
		t.Fatal("unknown key should fail")
	}
}

// An effort tier that is refused at load has to be refused here too:
// /config used to write an unusable tier to the file and ignore it for
// the rest of the session that set it.
func TestApplyKVEffort(t *testing.T) {
	cfg := &Config{}
	if v, err := ApplyKV(cfg, "effort", "xhigh"); err != nil || v != "xhigh" || cfg.Effort != "xhigh" {
		t.Fatalf("ApplyKV = %v, %v; cfg = %q", v, err, cfg.Effort)
	}
	if _, err := ApplyKV(cfg, "effort", "ultra"); err == nil {
		t.Fatal("ultra is not a tier")
	}
	if cfg.Effort != "xhigh" {
		t.Fatalf("a refused tier changed the config: %q", cfg.Effort)
	}
	if _, err := ApplyKV(cfg, "effort", ""); err != nil || cfg.Effort != "" {
		t.Fatalf("empty is the provider's own default: %v, %q", err, cfg.Effort)
	}
}

// The decisions keys build the block when there is none, and what they
// build has to survive the next load: a block without a pinned model is
// ErrBadConfig, so /jev on cannot write one.
func TestApplyKVBuildsAValidDecisionsBlock(t *testing.T) {
	cfg := &Config{}
	if _, err := ApplyKV(cfg, "decisions.triage", "on"); err != nil {
		t.Fatal(err)
	}
	if cfg.Decisions == nil || !cfg.Decisions.Triage {
		t.Fatalf("triage did not land: %+v", cfg.Decisions)
	}
	if err := cfg.Decisions.Validate(); err != nil {
		t.Fatalf("the block /config wrote does not load: %v", err)
	}
	if !cfg.Decisions.On() {
		t.Fatal("a block with no enabled key is on")
	}
	if _, err := ApplyKV(cfg, "decisions.enabled", "off"); err != nil {
		t.Fatal(err)
	}
	if cfg.Decisions.On() || cfg.Decisions.ConsumerOn("triage") {
		t.Fatal("the master switch must take the consumers with it")
	}
	if !cfg.Decisions.Triage {
		t.Fatal("switching off must not forget which consumers were on")
	}
}

func TestApplyKVDecisionsRefusesWhatLoadWouldRefuse(t *testing.T) {
	cfg := &Config{}
	for _, tc := range []struct{ key, val string }{
		{"decisions.model", "jev-latest"},
		{"decisions.model", "jev-1.13"},
		{"decisions.timeout_ms", "3000"},
		{"decisions.timeout_ms", "-1"},
		{"decisions.endpoint", "typesafe"},
		{"decisions.triage", "maybe"},
	} {
		if _, err := ApplyKV(cfg, tc.key, tc.val); err == nil {
			t.Errorf("%s=%s was accepted", tc.key, tc.val)
		}
	}
	for _, tc := range []struct{ key, val string }{
		{"decisions.model", "jev-1.13.0"},
		{"decisions.model", "~typesafe/jev-latest"},
		{"decisions.timeout_ms", "2000"},
		{"decisions.endpoint", "openrouter"},
	} {
		if _, err := ApplyKV(cfg, tc.key, tc.val); err != nil {
			t.Errorf("%s=%s: %v", tc.key, tc.val, err)
		}
	}
	if cfg.Decisions.BaseURL != DecisionEndpoints[0].BaseURL || cfg.Decisions.KeyEnv() != DecisionEndpoints[0].KeyEnv {
		t.Fatalf("the endpoint did not land: %+v", cfg.Decisions)
	}
	if got := cfg.Decisions.EndpointName(); got != "openrouter" {
		t.Fatalf("endpoint reads back as %q", got)
	}
}

// The feature keys the slots exist for.
func TestApplyKVFeatureKeys(t *testing.T) {
	cfg := &Config{}
	if _, err := ApplyKV(cfg, "escalate_after", "2"); err != nil || cfg.EscalateAfter != 2 {
		t.Fatalf("escalate_after: %v, %d", err, cfg.EscalateAfter)
	}
	if _, err := ApplyKV(cfg, "escalate_after", "0"); err != nil || cfg.EscalateAfter != 0 {
		t.Fatalf("zero is off: %v, %d", err, cfg.EscalateAfter)
	}
	if _, err := ApplyKV(cfg, "escalate_after", "-1"); err == nil {
		t.Fatal("a negative round count was accepted")
	}
	if _, err := ApplyKV(cfg, "stale_check_nudge", "on"); err != nil || !cfg.StaleCheckNudge {
		t.Fatalf("stale_check_nudge: %v, %v", err, cfg.StaleCheckNudge)
	}
	if _, err := ApplyKV(cfg, "advisor_mode", "always"); err != nil || cfg.AdvisorMode != "always" {
		t.Fatalf("advisor_mode: %v, %q", err, cfg.AdvisorMode)
	}
	if _, err := ApplyKV(cfg, "advisor_mode", "sometimes"); err == nil {
		t.Fatal("sometimes is not a mode")
	}
	if _, err := ApplyKV(cfg, "vision_model", "claude-opus-5"); err != nil || cfg.VisionModel != "claude-opus-5" {
		t.Fatalf("vision_model: %v, %q", err, cfg.VisionModel)
	}
}
