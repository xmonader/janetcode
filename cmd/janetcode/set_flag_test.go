package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
)

// setFlagHome points the config at a temporary directory holding one
// usable provider, so the test reads the machine's own config file no
// more than any other test here does.
func setFlagHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	dir := filepath.Join(home, ".config", "janetcode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"provider":"anthropic","model":"claude-opus-5","providers":{"anthropic":{"type":"anthropic","api_key":"k"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --set is /config for a run nobody is sitting in front of: the same
// catalogue, and it reaches the config the session is built from.
func TestSetFlagAppliesConfigKeys(t *testing.T) {
	setFlagHome(t)
	o := &options{sets: stringList{
		"escalate_after=2",
		"strong_model=qwen/qwen3.7-plus",
		"stale_check_nudge=true",
		"advisor_mode=always",
		"vision_model=sees/things",
		"decisions.triage=true",
	}}
	cfg, _, err := loadConfig(o)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.EscalateAfter != 2 || cfg.StrongModel != "qwen/qwen3.7-plus" || !cfg.StaleCheckNudge {
		t.Fatalf("cascade keys: after=%d strong=%q nudge=%v", cfg.EscalateAfter, cfg.StrongModel, cfg.StaleCheckNudge)
	}
	if cfg.AdvisorMode != "always" || cfg.VisionModel != "sees/things" {
		t.Fatalf("advisor=%q vision=%q", cfg.AdvisorMode, cfg.VisionModel)
	}
	if !cfg.Decisions.ConsumerOn("triage") {
		t.Fatalf("decisions = %+v", cfg.Decisions)
	}
	// The block --set built has to be one the next load accepts.
	if err := cfg.Decisions.Validate(); err != nil {
		t.Fatalf("the block --set wrote will not load: %v", err)
	}
	// And the slot resolver sees what was set, with the rest falling
	// back to the session's model.
	if _, m := cfg.Slot("strong"); m != "qwen/qwen3.7-plus" {
		t.Fatalf("strong slot = %q", m)
	}
	if _, m := cfg.Slot("advisor"); m != cfg.Model {
		t.Fatalf("an unset slot must be the main model, got %q", m)
	}
}

// A bad key or value stops the run rather than being ignored: a
// benchmark that misspells a knob must not quietly measure the default.
func TestSetFlagRefusesJunk(t *testing.T) {
	setFlagHome(t)
	for _, kv := range []string{"escalate_after", "no_such_key=1", "escalate_after=-1", "decisions.model=jev-latest"} {
		if _, _, err := loadConfig(&options{sets: stringList{kv}}); err == nil {
			t.Errorf("--set %s was accepted", kv)
		} else if !strings.Contains(err.Error(), "set") && !strings.Contains(err.Error(), "pinned") {
			t.Errorf("--set %s: %v", kv, err)
		}
	}
}

// The catalogue --set takes is the catalogue /config takes; neither has
// keys the other does not.
func TestSetFlagUsesTheConfigCatalogue(t *testing.T) {
	for _, k := range []string{"route_cheap", "strong_model", "advisor_model", "vision_model", "escalate_after", "advisor_mode", "stale_check_nudge", "decisions.enabled", "decisions.triage"} {
		found := false
		for _, have := range config.SettableKeys {
			found = found || have == k
		}
		if !found {
			t.Errorf("%q is not settable, so --set cannot reach it", k)
		}
	}
}
