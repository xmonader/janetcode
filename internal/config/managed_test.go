package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/managed"
)

func managedEnv(t *testing.T, policy string, extra map[string]string) func(string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(p, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{managed.EnvFile: p, "ANTHROPIC_API_KEY": "sk"}
	for k, v := range extra {
		m[k] = v
	}
	return env(m)
}

// The policy's settings override the user's file, the environment and
// the command line, and /config and --set cannot change them.
func TestManagedSettingsOverrideAndLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"effort": "low", "auto_approve": true, "sandbox": "off"}`), 0o600)
	cfg, err := load(path, managedEnv(t, `{"settings": {"effort": "high"}, "disabled_modes": ["yolo"], "sandbox": "workspace"}`, map[string]string{"JANETCODE_EFFORT": "medium"}), Overrides{Effort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Effort != "high" || cfg.AutoApprove || cfg.Sandbox != "workspace" {
		t.Fatalf("effort %q auto_approve %v sandbox %q; want the policy's", cfg.Effort, cfg.AutoApprove, cfg.Sandbox)
	}
	for _, k := range []string{"effort", "auto_approve", "sandbox"} {
		if _, err := ApplyKV(cfg, k, "low"); err == nil || !strings.Contains(err.Error(), "managed policy") {
			t.Errorf("/config %s: err = %v, want a managed-policy refusal", k, err)
		}
	}
	if _, err := ApplyKV(cfg, "max_tokens", "1000"); err != nil {
		t.Errorf("an unmanaged key refused: %v", err)
	}
}

func TestManagedRefusesYoloFlag(t *testing.T) {
	_, err := load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, `{"disabled_modes": ["yolo"]}`, nil), Overrides{AutoApprove: true})
	if !errors.Is(err, ErrBadOverride) || !strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("err = %v, want a usage error naming --yolo", err)
	}
}

func TestManagedAllowedProvidersAndModels(t *testing.T) {
	pol := `{"allowed_providers": ["anthropic"], "allowed_models": ["claude-*"]}`
	if _, err := load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, pol, nil), Overrides{Model: "claude-opus-5"}); err != nil {
		t.Fatalf("allowed model refused: %v", err)
	}
	_, err := load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, pol, nil), Overrides{Model: "gpt-5"})
	if !errors.Is(err, ErrBadOverride) || !strings.Contains(err.Error(), `model "gpt-5"`) {
		t.Fatalf("err = %v, want the model refused", err)
	}
	_, err = load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, pol, map[string]string{"OPENAI_API_KEY": "k"}), Overrides{Provider: "openai", Model: "claude-x"})
	if err == nil || !strings.Contains(err.Error(), `provider "openai"`) {
		t.Fatalf("err = %v, want the provider refused", err)
	}
	cfg, _ := load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, pol, nil), Overrides{})
	if _, err := ApplyKV(cfg, "model", "gpt-5"); err == nil {
		t.Error("/config model gpt-5 was accepted")
	}
}

// allowed_models is "the only models a session may use", but only the
// session's own model was checked. A model slot (strong, advisor, vision,
// cheap) or a fallback model runs calls of its own, so naming a forbidden
// model there, in the file or with /config, used it anyway.
func TestManagedAllowedModelsCoverTheSlots(t *testing.T) {
	pol := `{"allowed_models": ["claude-*"]}`
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"strong_model": "gpt-5", "advisor_model": "claude-opus-5", "fallback_models": ["gpt-5-mini", "claude-haiku-5"]}`), 0o600)
	cfg, err := load(path, managedEnv(t, pol, nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StrongModel != "" || cfg.AdvisorModel != "claude-opus-5" {
		t.Errorf("strong %q advisor %q: want the forbidden one dropped and the allowed one kept", cfg.StrongModel, cfg.AdvisorModel)
	}
	if len(cfg.FallbackModels) != 1 || cfg.FallbackModels[0] != "claude-haiku-5" {
		t.Errorf("fallback_models = %v, want only the allowed one", cfg.FallbackModels)
	}
	for _, k := range []string{"strong_model", "advisor_model", "vision_model", "route_cheap"} {
		if _, err := ApplyKV(cfg, k, "gpt-5"); err == nil || !strings.Contains(err.Error(), "managed policy") {
			t.Errorf("/config %s gpt-5: err = %v, want a managed-policy refusal", k, err)
		}
		if _, err := ApplyKV(cfg, k, ""); err != nil {
			t.Errorf("/config %s cleared: %v", k, err)
		}
	}
	if _, err := ApplyKV(cfg, "strong_model", "claude-opus-5"); err != nil {
		t.Errorf("an allowed slot model refused: %v", err)
	}
}

func TestManagedBadSettingsKey(t *testing.T) {
	_, err := load(filepath.Join(t.TempDir(), "none.json"), managedEnv(t, `{"settings": {"no_such_key": 1}}`, nil), Overrides{})
	if !errors.Is(err, ErrBadConfig) {
		t.Fatalf("err = %v, want a bad-config error", err)
	}
}
