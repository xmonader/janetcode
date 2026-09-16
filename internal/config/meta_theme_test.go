package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// SaveModelMeta short-circuits when nothing changed. ModelMeta holds a
// *llm.Price, so comparing records with == compared the pointers: two
// records with identical prices never matched, and every /connect and
// /model rewrote config.json for no change.
func TestSaveModelMetaSkipsAnIdenticalRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	meta := ModelMeta{ContextWindow: 200000, MaxOutput: 8192, Price: &llm.Price{Input: 1, Output: 2}}
	if err := SaveModelMeta(path, "m", meta); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A structurally identical record built separately: a different
	// pointer holding the same price, which is what a second load gives.
	same := ModelMeta{ContextWindow: 200000, MaxOutput: 8192, Price: &llm.Price{Input: 1, Output: 2}}
	if err := SaveModelMeta(path, "m", same); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("an unchanged record rewrote the file:\n%s\n%s", before, after)
	}
	// A real change still lands.
	changed := ModelMeta{ContextWindow: 200000, MaxOutput: 8192, Price: &llm.Price{Input: 9, Output: 2}}
	if err := SaveModelMeta(path, "m", changed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(after) {
		t.Fatal("a changed price was not written")
	}
}

// sameMeta handles a nil price on either side.
func TestSameMeta(t *testing.T) {
	p := &llm.Price{Input: 1, Output: 2}
	q := &llm.Price{Input: 1, Output: 2}
	r := &llm.Price{Input: 3, Output: 2}
	for _, c := range []struct {
		a, b ModelMeta
		want bool
	}{
		{ModelMeta{}, ModelMeta{}, true},
		{ModelMeta{Price: p}, ModelMeta{Price: q}, true},
		{ModelMeta{Price: p}, ModelMeta{Price: r}, false},
		{ModelMeta{Price: p}, ModelMeta{}, false},
		{ModelMeta{}, ModelMeta{Price: p}, false},
		{ModelMeta{ContextWindow: 1}, ModelMeta{ContextWindow: 2}, false},
		{ModelMeta{MaxOutput: 1}, ModelMeta{MaxOutput: 2}, false},
	} {
		if got := sameMeta(c.a, c.b); got != c.want {
			t.Errorf("sameMeta(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// /config theme refuses a name nothing can resolve, for the same reason
// effort does: writing it would make the next start fail outright.
func TestApplyKVValidatesTheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"gruvbox"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyKV(cfg, "theme", "nosuchtheme"); err == nil {
		t.Fatal("an unknown theme was accepted")
	}
	if cfg.Theme != "gruvbox" {
		t.Fatalf("a refused theme was stored anyway: %q", cfg.Theme)
	}
	for _, ok := range []string{"", "auto", "dark", "light", "nord"} {
		if _, err := ApplyKV(cfg, "theme", ok); err != nil {
			t.Errorf("ApplyKV(theme=%q) = %v", ok, err)
		}
	}
	// With no loaded theme set there is nothing to check against, so the
	// modes are enforced and the name is left to load to judge.
	if _, err := ApplyKV(&Config{}, "theme", "whatever"); err != nil {
		t.Errorf("a config with no theme set refused a name: %v", err)
	}
}
