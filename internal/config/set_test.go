package config

import "testing"

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
