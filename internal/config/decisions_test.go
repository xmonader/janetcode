package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decisions block is validated at load: a typo would otherwise be
// discovered as slow turns or an alias that silently retunes a
// threshold when TypeSafe ships a new version.
func TestDecisionsLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keyed := env(map[string]string{"ANTHROPIC_API_KEY": "k"})

	// A good block loads with its fields.
	cfg, err := load(write(`{"decisions":{"model":"jev-1.13.0","triage":true,"timeout_ms":500}}`),
		keyed, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decisions == nil || cfg.Decisions.Model != "jev-1.13.0" || !cfg.Decisions.Triage || cfg.Decisions.TimeoutMs != 500 {
		t.Fatalf("decisions = %+v", cfg.Decisions)
	}
	if cfg.Decisions.KeyEnv() != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q", cfg.Decisions.KeyEnv())
	}
	// An overridden key env is honoured.
	cfg, err = load(write(`{"decisions":{"model":"jev-1.13.0","api_key_env":"MY_KEY"}}`), keyed, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decisions.KeyEnv() != "MY_KEY" {
		t.Fatalf("KeyEnv = %q", cfg.Decisions.KeyEnv())
	}

	bad := []struct{ name, body, want string }{
		{"alias model", `{"decisions":{"model":"jev-latest"}}`, "not a pinned version"},
		{"no model", `{"decisions":{"triage":true}}`, "not a pinned version"},
		{"unpinned version", `{"decisions":{"model":"jev-1.13"}}`, "not a pinned version"},
		{"timeout over the ceiling", `{"decisions":{"model":"jev-1.13.0","timeout_ms":3000}}`, "timeout_ms"},
		{"negative timeout", `{"decisions":{"model":"jev-1.13.0","timeout_ms":-5}}`, "timeout_ms"},
		{"http off loopback", `{"decisions":{"model":"jev-1.13.0","base_url":"http://api.example.com"}}`, "https"},
		{"not a url", `{"decisions":{"model":"jev-1.13.0","base_url":"api.typesafe.ai"}}`, "https"},
		// "localhost:80" here is a user name; the host is off loopback.
		{"loopback as userinfo", `{"decisions":{"model":"jev-1.13.0","base_url":"http://localhost:80@api.example.com/v1"}}`, "https"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(write(c.body), keyed, Overrides{})
			if err == nil || !errors.Is(err, ErrBadConfig) {
				t.Fatalf("err = %v, want ErrBadConfig", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}

	// Loopback http is allowed: that is the test-server shape.
	if _, err := load(write(`{"decisions":{"model":"jev-1.13.0","base_url":"http://127.0.0.1:9000"}}`), keyed, Overrides{}); err != nil {
		t.Fatalf("loopback http refused: %v", err)
	}
	if _, err := load(write(`{"decisions":{"model":"jev-1.13.0","base_url":"http://[::1]:9000/v1"}}`), keyed, Overrides{}); err != nil {
		t.Fatalf("IPv6 loopback http refused: %v", err)
	}
	// The block is present but inert until a consumer is on.
	cfg, err = load(write(`{"decisions":{"model":"jev-1.13.0"}}`), keyed, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decisions.Triage || cfg.Decisions.Trajectory || cfg.Decisions.Intake {
		t.Fatalf("consumers default on: %+v", cfg.Decisions)
	}
}
