package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAskWait(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":        time.Hour, // the default passed in
		"forever": AskWaitForever,
		"Never":   AskWaitForever,
		"0":       0,
		"45":      45 * time.Second,
		"90s":     90 * time.Second,
		"5m":      5 * time.Minute,
	} {
		got, err := ParseAskWait("ask_timeout", in, time.Hour)
		if err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"-5", "soon", "-1s", "5 minutes"} {
		if _, err := ParseAskWait("ask_timeout", bad, 0); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The defaults: an interactive question waits as long as it takes, an
// unattended one two minutes.
func TestAskWaitsDefaults(t *testing.T) {
	in, un, err := (&Config{}).AskWaits()
	if err != nil || in != AskWaitForever || un != DefaultAskTimeoutUnattended {
		t.Fatalf("defaults = %v, %v, %v", in, un, err)
	}
	in, un, err = (&Config{AskTimeout: "10m", AskTimeoutUnattended: "0"}).AskWaits()
	if err != nil || in != 10*time.Minute || un != 0 {
		t.Fatalf("set = %v, %v, %v", in, un, err)
	}
}

// A value the parser cannot read stops the load with the key named,
// rather than leaving questions to wait forever on a typo.
func TestBadAskTimeoutRefusedAtLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"ask_timeout_unattended":"soon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := load(p, env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{})
	if !errors.Is(err, ErrBadConfig) || !strings.Contains(err.Error(), "ask_timeout_unattended") {
		t.Fatalf("a config with ask_timeout_unattended \"soon\" loaded: %v", err)
	}
}
