package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/keymap"
)

// A bad binding is a config error at start, naming the action, not a key
// that silently does nothing; a good one reaches the resolved table.
func TestKeybindingsAreResolvedAtLoad(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	e := env(map[string]string{"ANTHROPIC_API_KEY": "k"})
	cfg, err := load(write(`{"keybindings": {"effort": "f2"}, "vim_mode": true}`), e, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.KeyMap.Lookup("f2"); a != keymap.Effort || !cfg.VimMode {
		t.Fatalf("f2 = %q, vim = %v", a, cfg.VimMode)
	}
	cfg, err = load(write(`{}`), e, Overrides{})
	if err != nil || cfg.KeyMap == nil {
		t.Fatalf("no keybindings: map %v, err %v", cfg.KeyMap, err)
	}
	_, err = load(write(`{"keybindings": {"effort": "ctrl+t"}}`), e, Overrides{})
	if !errors.Is(err, ErrBadConfig) || !strings.Contains(err.Error(), "bound to both") {
		t.Fatalf("a conflict loaded: %v", err)
	}
	if _, err := ApplyKV(cfg, "vim_mode", "true"); err != nil || !cfg.VimMode {
		t.Fatalf("/config vim_mode=true: %v", err)
	}
}
