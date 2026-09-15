package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
)

// protected_paths is read from the config file and appended to the
// defaults, so a project can add a path of its own without being able to
// take one of the defaults out.
func TestProtectedPathsFromConfig(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"protected_paths":["~/work/deploy/**","/srv/secrets.env"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, env(map[string]string{"ANTHROPIC_API_KEY": "sk"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProtectedPaths) != 2 {
		t.Fatalf("protected_paths = %v", cfg.ProtectedPaths)
	}
	p := perm.NewProtected(cfg.ProtectedPaths)
	for _, abs := range []string{
		filepath.Join(home, "work", "deploy", "prod.yaml"),
		"/srv/secrets.env",
		filepath.Join(home, ".zshenv"),
	} {
		if _, ok := p.MatchPath(abs); !ok {
			t.Errorf("%s is not protected", abs)
		}
	}
}
