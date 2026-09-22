package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every key the interface can now write has to survive the file: set it
// the way /config does, read the file back the way a session start does,
// and get the same value. A key that writes and does not load is a
// setting that silently reverts on the next start.
func TestNewKeysRoundTripThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	e := env(map[string]string{"ANTHROPIC_API_KEY": "sk"})
	cfg, err := load(path, e, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	writes := []struct{ key, val string }{
		{"route_cheap", "cheap/one"},
		{"strong_model", "strong/two"},
		{"advisor_model", "advisor/three"},
		{"vision_model", "vision/four"},
		{"escalate_after", "2"},
		{"advisor_mode", "always"},
		{"advisor_max_consults", "3"},
		{"stale_check_nudge", "true"},
		{"decisions.enabled", "true"},
		{"decisions.model", "jev-1.13.0"},
		{"decisions.endpoint", "openrouter"},
		{"decisions.timeout_ms", "900"},
		{"decisions.triage", "true"},
		{"decisions.intake", "true"},
		{"decisions.intake_route", "true"},
		{"decisions.session_outcome", "true"},
	}
	for _, w := range writes {
		applied, err := ApplyKV(cfg, w.key, w.val)
		if err != nil {
			t.Fatalf("%s=%s: %v", w.key, w.val, err)
		}
		// The same rule settingsapply.Apply follows: a key inside the
		// block writes the block.
		key, value := w.key, applied
		if strings.HasPrefix(key, "decisions.") {
			key, value = "decisions", cfg.Decisions
		}
		if err := UpdateKey(path, key, value); err != nil {
			t.Fatalf("%s: %v", w.key, err)
		}
	}
	back, err := load(path, e, Overrides{})
	if err != nil {
		t.Fatalf("the file the interface wrote does not load: %v", err)
	}
	if back.RouteCheap != "cheap/one" || back.StrongModel != "strong/two" || back.AdvisorModel != "advisor/three" || back.VisionModel != "vision/four" {
		t.Fatalf("slots: %+v", []string{back.RouteCheap, back.StrongModel, back.AdvisorModel, back.VisionModel})
	}
	if back.EscalateAfter != 2 || back.AdvisorMode != "always" || back.AdvisorMaxConsults != 3 || !back.StaleCheckNudge {
		t.Fatalf("features: after=%d mode=%q max=%d nudge=%v", back.EscalateAfter, back.AdvisorMode, back.AdvisorMaxConsults, back.StaleCheckNudge)
	}
	d := back.Decisions
	if d == nil || !d.On() || d.Model != "jev-1.13.0" || d.TimeoutMs != 900 || d.EndpointName() != "openrouter" {
		t.Fatalf("decisions: %+v", d)
	}
	for _, c := range DecisionConsumers {
		if !d.ConsumerOn(c) {
			t.Fatalf("consumer %s did not survive the file", c)
		}
	}
	// And the switch: off is stored, loads as off, and keeps the rest.
	if _, err := ApplyKV(back, "decisions.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateKey(path, "decisions", back.Decisions); err != nil {
		t.Fatal(err)
	}
	off, err := load(path, e, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if off.Decisions.On() || off.Decisions.ConsumerOn("triage") || !off.Decisions.Triage {
		t.Fatalf("switched off: %+v", off.Decisions)
	}
}

// Nothing new configured reads exactly as it did: every slot is the
// session's model, every feature is off, and the file keeps no key for
// any of them.
func TestNothingConfiguredIsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"anthropic","model":"m","providers":{"anthropic":{"type":"anthropic","api_key":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, env(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range SlotNames {
		if !cfg.SlotIsMain(s) {
			t.Fatalf("slot %s is not the main model", s)
		}
		if _, model := cfg.Slot(s); model != "m" {
			t.Fatalf("slot %s = %q", s, model)
		}
	}
	if cfg.EscalateAfter != 0 || cfg.AdvisorMode != "" || cfg.StaleCheckNudge || cfg.VisionModel != "" {
		t.Fatalf("a feature is on with nothing configured: %+v", cfg)
	}
	if cfg.Decisions.On() || cfg.Decisions.AnyConsumerOn() {
		t.Fatal("decisions is on with no block")
	}
}
